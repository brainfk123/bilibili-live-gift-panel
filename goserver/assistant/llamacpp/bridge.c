//go:build windows && cgo && llamacpp

#include "bridge.h"

#include "llama.h"
#include "ggml-cpu.h"

#include <stdatomic.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

struct blgp_llama_engine {
    struct llama_model * model;
    struct llama_context * context;
    struct llama_sampler * sampler;
    struct ggml_threadpool * threadpool;
    atomic_bool abort_requested;
    int32_t max_tokens;
    int32_t generated_tokens;
    char error[512];
};

static bool backend_initialized = false;

static void clear_error(blgp_llama_engine * engine) {
    if (engine != NULL) {
        engine->error[0] = '\0';
    }
}

static void set_error(blgp_llama_engine * engine, const char * message) {
    if (engine == NULL) {
        return;
    }
    snprintf(engine->error, sizeof(engine->error), "%s", message == NULL ? "unknown native error" : message);
}

static bool load_progress(float progress, void * user_data) {
    (void) progress;
    blgp_llama_engine * engine = (blgp_llama_engine *) user_data;
    return engine != NULL && !atomic_load_explicit(&engine->abort_requested, memory_order_relaxed);
}

static bool decode_aborted(void * user_data) {
    blgp_llama_engine * engine = (blgp_llama_engine *) user_data;
    return engine == NULL || atomic_load_explicit(&engine->abort_requested, memory_order_relaxed);
}

static void release_loaded_model(blgp_llama_engine * engine) {
    if (engine == NULL) {
        return;
    }
    if (engine->sampler != NULL) {
        llama_sampler_free(engine->sampler);
        engine->sampler = NULL;
    }
    if (engine->context != NULL && engine->threadpool != NULL) {
        llama_detach_threadpool(engine->context);
    }
    if (engine->context != NULL) {
        llama_free(engine->context);
        engine->context = NULL;
    }
    if (engine->threadpool != NULL) {
        ggml_threadpool_free(engine->threadpool);
        engine->threadpool = NULL;
    }
    if (engine->model != NULL) {
        llama_model_free(engine->model);
        engine->model = NULL;
    }
}

blgp_llama_engine * blgp_llama_new(void) {
    blgp_llama_engine * engine = (blgp_llama_engine *) calloc(1, sizeof(blgp_llama_engine));
    if (engine != NULL) {
        atomic_init(&engine->abort_requested, false);
    }
    return engine;
}

void blgp_llama_delete(blgp_llama_engine * engine) {
    if (engine == NULL) {
        return;
    }
    release_loaded_model(engine);
    free(engine);
}

void blgp_llama_abort(blgp_llama_engine * engine) {
    if (engine != NULL) {
        atomic_store_explicit(&engine->abort_requested, true, memory_order_relaxed);
    }
}

int blgp_llama_load(
        blgp_llama_engine * engine,
        const char * model_path,
        int32_t context_size,
        int32_t threads) {
    if (engine == NULL || model_path == NULL || model_path[0] == '\0') {
        return -1;
    }
    clear_error(engine);
    release_loaded_model(engine);
    atomic_store_explicit(&engine->abort_requested, false, memory_order_relaxed);

    if (context_size < 256 || threads < 1) {
        set_error(engine, "invalid context size or thread count");
        return -1;
    }
    if (!backend_initialized) {
        llama_backend_init();
        backend_initialized = true;
    }

    struct llama_model_params model_params = llama_model_default_params();
    model_params.n_gpu_layers = 0;
    model_params.use_mmap = true;
    model_params.use_mlock = false;
    model_params.progress_callback = load_progress;
    model_params.progress_callback_user_data = engine;
    engine->model = llama_model_load_from_file(model_path, model_params);
    if (engine->model == NULL) {
        set_error(engine, atomic_load_explicit(&engine->abort_requested, memory_order_relaxed)
            ? "model load cancelled"
            : "unable to load GGUF model");
        return -1;
    }

    struct llama_context_params context_params = llama_context_default_params();
    context_params.n_ctx = (uint32_t) context_size;
    context_params.n_batch = (uint32_t) (context_size < 512 ? context_size : 512);
    context_params.n_ubatch = context_params.n_batch;
    context_params.n_threads = threads;
    context_params.n_threads_batch = threads;
    context_params.abort_callback = decode_aborted;
    context_params.abort_callback_data = engine;
    context_params.no_perf = true;
    engine->context = llama_init_from_model(engine->model, context_params);
    if (engine->context == NULL) {
        set_error(engine, "unable to create llama context");
        release_loaded_model(engine);
        return -1;
    }
    struct ggml_threadpool_params threadpool_params = ggml_threadpool_params_default(threads);
    threadpool_params.prio = GGML_SCHED_PRIO_LOW;
    threadpool_params.poll = 0;
    engine->threadpool = ggml_threadpool_new(&threadpool_params);
    if (engine->threadpool == NULL) {
        set_error(engine, "unable to create low-priority CPU threadpool");
        release_loaded_model(engine);
        return -1;
    }
    // A NULL batch pool makes llama.cpp reuse this same low-priority pool for
    // both prompt evaluation and single-token generation.
    llama_attach_threadpool(engine->context, engine->threadpool, NULL);
    return 0;
}

int blgp_llama_generate_start(
        blgp_llama_engine * engine,
        const char * prompt,
        int32_t prompt_bytes,
        int32_t max_tokens,
        float temperature,
        float top_p,
        int32_t top_k) {
    if (engine == NULL || engine->model == NULL || engine->context == NULL) {
        if (engine != NULL) {
            set_error(engine, "model is not loaded");
        }
        return -1;
    }
    if (prompt == NULL || prompt_bytes <= 0 || max_tokens <= 0) {
        set_error(engine, "prompt and max_tokens must be non-empty");
        return -1;
    }

    clear_error(engine);
    atomic_store_explicit(&engine->abort_requested, false, memory_order_relaxed);
    if (engine->sampler != NULL) {
        llama_sampler_free(engine->sampler);
        engine->sampler = NULL;
    }
    llama_memory_t memory = llama_get_memory(engine->context);
    if (memory != NULL) {
        llama_memory_clear(memory, true);
    }

    const struct llama_vocab * vocab = llama_model_get_vocab(engine->model);
    int32_t prompt_count = llama_tokenize(vocab, prompt, prompt_bytes, NULL, 0, true, true);
    if (prompt_count >= 0) {
        set_error(engine, "unable to determine prompt token count");
        return -1;
    }
    prompt_count = -prompt_count;
    if ((uint32_t) (prompt_count + max_tokens) > llama_n_ctx(engine->context)) {
        set_error(engine, "prompt and response exceed the configured context size");
        return -1;
    }

    llama_token * tokens = (llama_token *) malloc((size_t) prompt_count * sizeof(llama_token));
    if (tokens == NULL) {
        set_error(engine, "unable to allocate prompt token buffer");
        return -1;
    }
    int32_t tokenized = llama_tokenize(vocab, prompt, prompt_bytes, tokens, prompt_count, true, true);
    if (tokenized != prompt_count) {
        free(tokens);
        set_error(engine, "unable to tokenize prompt");
        return -1;
    }

    const int32_t batch_size = (int32_t) llama_n_batch(engine->context);
    for (int32_t offset = 0; offset < prompt_count; offset += batch_size) {
        int32_t count = prompt_count - offset;
        if (count > batch_size) {
            count = batch_size;
        }
        struct llama_batch batch = llama_batch_get_one(tokens + offset, count);
        int32_t decode_result = llama_decode(engine->context, batch);
        if (decode_result != 0) {
            free(tokens);
            set_error(engine, atomic_load_explicit(&engine->abort_requested, memory_order_relaxed)
                ? "generation cancelled"
                : "unable to decode prompt");
            return -1;
        }
    }
    free(tokens);

    struct llama_sampler_chain_params sampler_params = llama_sampler_chain_default_params();
    sampler_params.no_perf = true;
    engine->sampler = llama_sampler_chain_init(sampler_params);
    if (engine->sampler == NULL) {
        set_error(engine, "unable to create sampler");
        return -1;
    }
    llama_sampler_chain_add(engine->sampler, llama_sampler_init_top_k(top_k));
    llama_sampler_chain_add(engine->sampler, llama_sampler_init_top_p(top_p, 1));
    llama_sampler_chain_add(engine->sampler, llama_sampler_init_temp(temperature));
    llama_sampler_chain_add(engine->sampler, llama_sampler_init_dist(LLAMA_DEFAULT_SEED));
    engine->max_tokens = max_tokens;
    engine->generated_tokens = 0;
    return 0;
}

int blgp_llama_next(blgp_llama_engine * engine, char * piece, int32_t capacity) {
    if (engine == NULL || engine->context == NULL || engine->model == NULL || engine->sampler == NULL) {
        if (engine != NULL) {
            set_error(engine, "generation has not started");
        }
        return -1;
    }
    if (piece == NULL || capacity <= 0) {
        set_error(engine, "invalid output buffer");
        return -1;
    }
    if (atomic_load_explicit(&engine->abort_requested, memory_order_relaxed)) {
        set_error(engine, "generation cancelled");
        return -1;
    }
    if (engine->generated_tokens >= engine->max_tokens) {
        return 0;
    }

    const struct llama_vocab * vocab = llama_model_get_vocab(engine->model);
    llama_token token = llama_sampler_sample(engine->sampler, engine->context, -1);
    if (llama_vocab_is_eog(vocab, token)) {
        return 0;
    }
    int32_t piece_bytes = llama_token_to_piece(vocab, token, piece, capacity, 0, true);
    if (piece_bytes < 0 || piece_bytes > capacity) {
        set_error(engine, "generated token exceeds output buffer");
        return -1;
    }

    struct llama_batch batch = llama_batch_get_one(&token, 1);
    int32_t decode_result = llama_decode(engine->context, batch);
    if (decode_result != 0) {
        set_error(engine, atomic_load_explicit(&engine->abort_requested, memory_order_relaxed)
            ? "generation cancelled"
            : "unable to decode generated token");
        return -1;
    }
    engine->generated_tokens++;
    return piece_bytes;
}

const char * blgp_llama_last_error(const blgp_llama_engine * engine) {
    if (engine == NULL || engine->error[0] == '\0') {
        return "unknown native error";
    }
    return engine->error;
}
