-- Native binaries as assets: drivers (.sys), DLLs, executables and other PE/
-- ELF files that OS/native-app pentest tasks track, versioned by the file's own
-- product version. Keyed by (name, version) so a patched clfs.sys 10.0.26100.9818
-- and 10.0.26100.9974 are two rows (the patch-diff target pairs).
--
-- Reuses existing columns:
--   app_name        -> file name (clfs.sys)
--   app_description -> product description (Common Log File System Driver)
--   category        -> kind slug: driver | dll | exe | library | firmware | other
--   extra           -> machine/host path, sha256, arch, OS build, signed, source
--   (no new columns needed; version lives in a dedicated one below)

ALTER TABLE assets ADD COLUMN IF NOT EXISTS version TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_binary
    ON assets(COALESCE(app_name,''), COALESCE(version,'')) WHERE type = 'binary';
CREATE INDEX IF NOT EXISTS idx_av2_binary_kind
    ON assets(category, app_name) WHERE type = 'binary';