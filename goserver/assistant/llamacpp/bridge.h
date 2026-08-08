#ifndef BLGP_LLAMA_BRIDGE_H
#define BLGP_LLAMA_BRIDGE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct blgp_llama_engine blgp_llama_engine;

blgp_llama_engine * blgp_llama_new(void);
void blgp_llama_delete(blgp_llama_engine * engine);
void blgp_llama_abort(blgp_llama_engine * engine);

int blgp_llama_load(
    blgp_llama_engine * engine,
    const char * model_path,
    int32_t context_size,
    int32_t threads);

int blgp_llama_generate_start(
    blgp_llama_engine * engine,
    const char * prompt,
    int32_t prompt_bytes,
    int32_t max_tokens,
    float temperature,
    float top_p,
    int32_t top_k);

// Returns the number of bytes written, 0 at EOG/max tokens, or -1 on error.
int blgp_llama_next(blgp_llama_engine * engine, char * piece, int32_t capacity);

const char * blgp_llama_last_error(const blgp_llama_engine * engine);

#ifdef __cplusplus
}
#endif

#endif
