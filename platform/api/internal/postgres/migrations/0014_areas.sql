-- Area supervisors, part 1a (contract block 6, "Area supervisors"): areas, their members and supervisors, and the
-- audit column that records the area a supervisor acted through. Never edit applied migrations.
--
-- Deployment ordering: the migrate Job must complete before the portal rolls out. New pods check the grants of this
-- migration at start-up and refuse to run (fail closed) against the old ones, and the other way round during a
-- rollback.

-- ALTER TABLE audit_events takes ACCESS EXCLUSIVE on a table platform-api writes (login audit, RevokeUser): do not
-- queue every writer behind a long transaction. The migration fails and is retried instead. Transaction-local, like
-- 0012.
SELECT set_config('lock_timeout', '5s', true);

-- ---- areas ----
-- No status: an area grants nothing by itself. It is deleted only when it has no members and no supervisors
-- (ON DELETE RESTRICT below).
CREATE TABLE areas (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    key        text        NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$' AND char_length(key) <= 63),
    name       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ---- area_members: one area per person, humans only ----
-- The kind pin and the composite foreign key are the pattern of group_members (0012); the constraint keeps the name
-- the portal maps to the request field user_id (referenceFields).
CREATE TABLE area_members (
    area_id  uuid        NOT NULL REFERENCES areas (id) ON DELETE RESTRICT,
    user_id  uuid        NOT NULL,
    kind     text        NOT NULL DEFAULT 'human' CONSTRAINT area_members_kind_check CHECK (kind = 'human'),
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id),
    CONSTRAINT area_members_user_id_fkey FOREIGN KEY (user_id, kind) REFERENCES users (id, kind) ON DELETE CASCADE
);
CREATE INDEX area_members_area_id_user_id_idx ON area_members (area_id, user_id);

-- ---- area_supervisors: several per area, several areas per person, humans only ----
CREATE TABLE area_supervisors (
    area_id  uuid        NOT NULL REFERENCES areas (id) ON DELETE RESTRICT,
    user_id  uuid        NOT NULL,
    kind     text        NOT NULL DEFAULT 'human' CONSTRAINT area_supervisors_kind_check CHECK (kind = 'human'),
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (area_id, user_id),
    CONSTRAINT area_supervisors_user_id_fkey FOREIGN KEY (user_id, kind) REFERENCES users (id, kind) ON DELETE CASCADE
);
CREATE INDEX area_supervisors_user_id_idx ON area_supervisors (user_id); -- the permission query

-- ---- audit: the area a supervisor acted through ----
-- No FK and no index, like token_id (0012): nothing filters by it yet.
ALTER TABLE audit_events ADD COLUMN area_id uuid;

-- ---- privileges: platform_portal ----
-- No UPDATE: area keys are immutable and there is no rename. The area DELETE takes no explicit lock (SELECT ... FOR
-- UPDATE would need UPDATE): it relies on ON DELETE RESTRICT. platform_runtime gets nothing on these tables, and
-- the table-wide INSERT on audit_events already covers the new column.
GRANT SELECT, INSERT, DELETE ON areas, area_members, area_supervisors TO platform_portal;
