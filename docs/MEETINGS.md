# Meetings

Set `LLM_PROVIDER=mock` and `TRANSCRIPTION_PROVIDER=mock` in local `.env`, then run
`docker compose up --build -d --wait`. Open `/meetings`, select a project and upload
a WAV, MP3, MP4 or WebM recording with one audio track. The offline transcriber returns a fixed sample
transcript with one decision and action; it does not recognize the recording's speech.
The media parser still validates the actual file and duration in mock mode.

The page plays private audio/video, searches and paginates the transcript, seeks when
a segment/evidence link is selected, and shows extracted suggestions. **Review and
approve into tasks** shows the proposed fields and exact quote before approval.
Decisions have the same review flow. Rejection remains available in the proposals
inbox. A queued or pending suggestion never creates a task or approved decision.

Defaults: 25,000,000 bytes per recording, up to 60 minutes, 600 reserved transcription
minutes per workspace over 24 hours, and ten uploads per user/workspace/hour. The
maximum duration is also bounded by the daily budget. Initial reservations are reduced
to rounded-up actual duration after probing; failed/uncertain attempts retain reservations.
Transcript storage is capped at 2,000 bounded segments. LLM extraction has its own
shared workspace token/cost limits. No automatic retry re-bills an unknown transcription.

Compose uses a private `meeting_media` volume: the API writes and the worker mounts
it read-only. Keep it with the database volumes. For standalone services, set an
absolute `MEDIA_ROOT` shared by API/worker and install FFmpeg/ffprobe on the worker. Storage
is behind `filestore.Store`; a future S3 adapter must retain workspace-key validation,
private objects and the authenticated media boundary. No public bucket is configured.

For real transcription, explicitly select `TRANSCRIPTION_PROVIDER=openai`, inject a
worker-only `OPENAI_API_KEY`, and set `TRANSCRIPTION_MODEL` and `TRANSCRIPTION_FORMAT`.
The adapter uses the [official file transcription endpoint](https://developers.openai.com/api/docs/guides/speech-to-text).
`json` text-only output becomes a whole-recording interval with an unknown speaker;
it does not claim precise speaker or word timestamps. A compatible `diarized_json`
model supplies speaker/start/end segments; `verbose_json` supplies timestamps with
unknown speakers when labels are absent. Select a compatible format for the model.
The current official guide marks the legacy diarization models for shutdown on
February 26, 2027; no deprecated model is selected automatically. Provider-generated
segments are validated against the probed duration. Long text blocks retain their
original interval when split, so seeking never pretends to have finer timing.

Configure an extraction model and explicit cost ceilings as described in `.env.example`.
Real recordings/transcripts intentionally leave the deployment for the chosen providers;
provider keys remain worker-only. Tests use deterministic fixtures and HTTP contracts.
Run `python scripts/meetings_smoke.py` against a mock-enabled stack to verify actual
media probing, storage, transcription, evidence, approval and audit end to end.
