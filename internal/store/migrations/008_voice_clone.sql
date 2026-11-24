-- kind: 'upstream' (a vendor voice id, e.g. ElevenLabs voice_id / Fish reference_id)
--     | 'sample'   (a local reference-audio file, used for zero-shot cloning)
ALTER TABLE voices ADD COLUMN kind TEXT NOT NULL DEFAULT 'upstream';
