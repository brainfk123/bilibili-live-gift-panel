# Task 12 report: Studio FFmpeg export jobs

## State machine

`loading -> editing -> exporting -> ready` is the success path.  Invalid source,
layer/create/poll failures go to `failed`; retry returns to `loading`; re-edit
returns to `editing` while retaining its media session.  `close` is terminal.

## Transition and stale-token rule

Each load, confirmation, and re-edit increments `transition`.  Async media,
layer, create, poll, and preview completions may update the view only when their
captured token is still current and the studio has not closed.  A created job
that arrives after its token has gone stale is DELETEd immediately.

## Export cancellation table

| Event | Abort | DELETE | UI result |
| --- | --- | --- | --- |
| close | current export signal | current job, or a late-created exact job | no post-close writes |
| re-edit | current export signal | current job | editor resumes without waiting for DELETE |
| retry/new confirmation | current export signal | current job | fresh source/export transition |
| export failure | current export signal | current job | stable failure message and retry |

`cancelExport` clears the recorded ID before best-effort DELETE, preventing a
duplicate cancellation.  Abort reasons are treated as silent cancellation;
only current non-abort failures call `showFailure` and `onError`.

## Scope correction

The brief required removal of `recordingCanvas` and also required the existing
view test in its GREEN command.  That test directly asserted the removed API,
but was omitted from the original owned-file list.  With explicit authorization,
`tests/gift-clip-studio-view.test.ts` received only the mechanical removal of
legacy-canvas assertions plus regressions that the canvas/API no longer exists.

## TDD evidence

- RED: the download helper test first failed because the module did not exist.
- GREEN: the helper passed after the MP4-only implementation.
- RED: Studio export lifecycle tests failed against the MediaRecorder path
  (missing `保存 MP4`, recorder failure message, and no export polling).
- GREEN: Studio now creates static PNG layers, creates and polls jobs, previews
  the same-origin MP4 URL, and passes lifecycle/stale/cancellation regressions.
