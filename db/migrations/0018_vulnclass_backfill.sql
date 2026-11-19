-- Normalize historical free-text vulnclass values into the canonical taxonomy.
-- The Go-side normalizer (db.NormalizeVulnClass) is the authority; this SQL
-- covers the values observed in this database. Anything unmatched stays as-is
-- (report_finding now rejects unknown values going forward).
UPDATE findings SET vulnclass = 'dos-crash' WHERE vulnclass IN ('dos', 'denial-of-service') OR vulnclass LIKE 'denial-of-service%';
UPDATE findings SET vulnclass = 'access-missing-authorization' WHERE vulnclass IN ('broken-access-control', 'improper_authorization');
UPDATE findings SET vulnclass = 'info-credential-leak' WHERE vulnclass = 'credential-disclosure';
UPDATE findings SET vulnclass = 'memory-integer-overflow' WHERE vulnclass IN ('integer_overflow', 'integer-overflow');
UPDATE findings SET vulnclass = 'memory-integer-underflow' WHERE vulnclass IN ('integer_underflow', 'integer-underflow');
UPDATE findings SET vulnclass = 'memory-oob-write' WHERE vulnclass LIKE 'kernel-oob-write%';
UPDATE findings SET vulnclass = 'info-disclosure' WHERE vulnclass LIKE 'information-disclosure%' OR vulnclass LIKE 'info-leak%';
UPDATE findings SET vulnclass = 'logic-flaw' WHERE vulnclass LIKE 'logic%' AND vulnclass NOT LIKE 'logic-insecure-design';
