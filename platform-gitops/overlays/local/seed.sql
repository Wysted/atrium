-- Development seed, run as platform_owner. Idempotent.
--
--   initiative platform (reserved)        apps: portal         host portal.apps.wysted.dev
--   initiative demo   roles viewer, editor, manager (manages_access)   apps: hello-a        host hello-a.apps.wysted.dev
--   initiative other  role  viewer           apps: hello-b        host hello-b.apps.wysted.dev
--
--   initiative observability  roles viewer, editor, admin (+ auditor, unmapped)   apps: grafana        host grafana.apps.wysted.dev
--                              (the role keys are the Grafana org roles; none has manages_access: Grafana admins do not manage grants)
--   alice  direct viewer grant on demo and on other     -> hello-a and hello-b (SSO); viewer on observability -> Grafana Viewer
--   bob    member of group demo-editors (editor on demo) -> hello-a only; hello-b is another initiative (no cross)
--   carol  exists, no grants                            -> 403 everywhere
--   dave   direct manager grant on demo (manages_access)  -> hello-a only; manages demo, not platform admin, nothing in other
--   bob    also direct `auditor` on observability (a role Grafana does not map): ForwardAuth lets him in, Grafana refuses (strict)
--   admin  also direct admin grant on observability     -> Grafana Admin (org role)
--   admin  PLATFORM ADMIN (platform_admins), DEVELOPMENT ONLY: lets the smoke exercise the portal's admin API with
--          the fake login. This file lives in the local overlay on purpose; never seed an admin from base.
-- App ids are the HELLO_APP_ID values in overlays/local/hello-{a,b}/kustomization.yaml and PORTAL_APP_ID /
-- PLATFORM_PORTAL_APP_ID in overlays/local/{portal,platform}/kustomization.yaml (the portal app id is fixed here); the Grafana
-- app id is the `aud` in expect_claims of overlays/local/monitoring/values.yaml.
BEGIN;

INSERT INTO initiatives (key, name) VALUES ('platform', 'Platform'), ('demo', 'Demo'), ('other', 'Other'),
    ('observability', 'Observability') ON CONFLICT (key) DO NOTHING;

INSERT INTO initiative_roles (initiative_id, key)
SELECT i.id, r.key FROM initiatives i
JOIN (VALUES ('demo', 'viewer'), ('demo', 'editor'), ('other', 'viewer'),
             ('observability', 'viewer'), ('observability', 'editor'), ('observability', 'admin'),
             ('observability', 'auditor')) AS r (initiative, key) ON r.initiative = i.key
ON CONFLICT (initiative_id, key) DO NOTHING;

INSERT INTO apps (id, initiative_id, name)
SELECT a.id, i.id, a.name FROM initiatives i
JOIN (VALUES ('app_01HQ3PD7RTA5M2XKWV9N8B4CYE', 'platform', 'portal'),
             ('app_01AFSXV5GZQ6FQ3BPFVTQWBBHV', 'demo', 'hello-a'),
             ('app_016XJSY1BRD79NEGNJBXQJT5KT', 'other', 'hello-b'),
             ('app_01J9Z3K7WQ4M8R2T5XV6N0B1HD', 'observability', 'grafana')) AS a (id, initiative, name) ON a.initiative = i.key
ON CONFLICT (id) DO NOTHING;

INSERT INTO deployments (host, app_id) VALUES
    ('portal.apps.wysted.dev', 'app_01HQ3PD7RTA5M2XKWV9N8B4CYE'),
    ('hello-a.apps.wysted.dev', 'app_01AFSXV5GZQ6FQ3BPFVTQWBBHV'),
    ('hello-b.apps.wysted.dev', 'app_016XJSY1BRD79NEGNJBXQJT5KT'),
    ('grafana.apps.wysted.dev', 'app_01J9Z3K7WQ4M8R2T5XV6N0B1HD')
ON CONFLICT (host) DO NOTHING;

-- google_sub format of the fake login: fake:<email>.
INSERT INTO users (google_sub, email, name) VALUES
    ('fake:alice@example.com', 'alice@example.com', 'alice'),
    ('fake:bob@example.com',   'bob@example.com',   'bob'),
    ('fake:carol@example.com', 'carol@example.com', 'carol'),
    ('fake:dave@example.com',  'dave@example.com',  'dave'),
    ('fake:admin@example.com', 'admin@example.com', 'admin')
ON CONFLICT (google_sub) DO NOTHING;

INSERT INTO platform_admins (user_id)
SELECT id FROM users WHERE google_sub = 'fake:admin@example.com'
ON CONFLICT DO NOTHING;

INSERT INTO groups (key, name) VALUES ('demo-editors', 'Demo editors') ON CONFLICT (key) DO NOTHING;

INSERT INTO group_members (group_id, user_id)
SELECT g.id, u.id FROM groups g, users u WHERE g.key = 'demo-editors' AND u.google_sub = 'fake:bob@example.com'
ON CONFLICT DO NOTHING;

-- The only role with manages_access in the seed (demo): dave's route to the initiative-manager scope.
INSERT INTO initiative_roles (initiative_id, key, manages_access)
SELECT i.id, 'manager', true FROM initiatives i WHERE i.key = 'demo'
ON CONFLICT (initiative_id, key) DO NOTHING;

-- Direct grants for alice.
INSERT INTO initiative_access (initiative_id, role_id, user_id)
SELECT r.initiative_id, r.id, u.id
FROM initiative_roles r
JOIN initiatives i ON i.id = r.initiative_id
JOIN users u ON u.google_sub = 'fake:alice@example.com'
WHERE r.key = 'viewer' AND i.key IN ('demo', 'other')
ON CONFLICT DO NOTHING;

-- Grafana: alice viewer, admin admin (the smoke checks both org roles).
INSERT INTO initiative_access (initiative_id, role_id, user_id)
SELECT r.initiative_id, r.id, u.id
FROM initiative_roles r
JOIN initiatives i ON i.id = r.initiative_id
JOIN (VALUES ('fake:alice@example.com', 'viewer'), ('fake:admin@example.com', 'admin')) AS g (sub, role) ON g.role = r.key
JOIN users u ON u.google_sub = g.sub
WHERE i.key = 'observability'
ON CONFLICT DO NOTHING;

-- Grafana: bob holds ONLY `auditor` there, a role outside role_attribute_path (smoke 19b: refused by role_attribute_strict).
INSERT INTO initiative_access (initiative_id, role_id, user_id)
SELECT r.initiative_id, r.id, u.id
FROM initiative_roles r
JOIN initiatives i ON i.id = r.initiative_id
JOIN users u ON u.google_sub = 'fake:bob@example.com'
WHERE i.key = 'observability' AND r.key = 'auditor'
ON CONFLICT DO NOTHING;

-- Direct manager grant for dave on demo only.
INSERT INTO initiative_access (initiative_id, role_id, user_id)
SELECT r.initiative_id, r.id, u.id
FROM initiative_roles r
JOIN initiatives i ON i.id = r.initiative_id
JOIN users u ON u.google_sub = 'fake:dave@example.com'
WHERE r.key = 'manager' AND i.key = 'demo'
ON CONFLICT DO NOTHING;

-- Group grant: demo-editors -> editor on demo.
INSERT INTO initiative_access (initiative_id, role_id, group_id)
SELECT r.initiative_id, r.id, g.id
FROM initiative_roles r
JOIN initiatives i ON i.id = r.initiative_id
JOIN groups g ON g.key = 'demo-editors'
WHERE r.key = 'editor' AND i.key = 'demo'
ON CONFLICT DO NOTHING;

COMMIT;
