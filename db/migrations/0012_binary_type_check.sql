-- Extend the assets type CHECK to admit 'binary' (the column constraint in
-- schema.sql already lists it for fresh databases; this brings existing ones).
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_type_check;
ALTER TABLE assets ADD CONSTRAINT assets_type_check CHECK (
    type IN ('root_domain','ip','subdomain','app','service','endpoint','binary')
);
