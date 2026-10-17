# 0010. Local areas with admin-assigned supervisors

- Status: Accepted
- Date: 2026-10-17

## Context and Problem

Offboarding a person's automation (their tokens and robots) should not wait for a platform admin, yet it must not give anyone control over access or over their peers.

## Considered Options

- Reuse groups: rejected, adding someone to an access group must never put them under a supervisor (privilege by side effect).
- A manager chain read from the IdP: not chosen.
- Reuse the initiative manager: rejected, managers administer access, supervisors administer people's credentials.
- A container defined in the portal, like Entra administrative units, Okta resource sets or Workspace OU-scoped roles (chosen).

## Decision Outcome

Areas, `area_members` (one area per person, humans only) and `area_supervisors` are their own tables, managed only by platform admins. A person is supervisable when they are a human member of an area, not a platform admin and supervise no area, so supervisors never reach themselves, each other or admins. A supervisor can list metadata, revoke tokens and change a robot's `owner_user_id` within their areas, never grants, groups or access. The capacity is browser-session only, evaluated from the database on every request, and out-of-scope targets answer 404.

## Consequences

- No hierarchy, no rename, no supervisor audit view or notifications in this design.
- Later, not designed: IdP sync of members (`area_members.source`, by Workspace `orgUnitPath` or a Google group), area hierarchy, renaming, an audit view for supervisors, notifications and `portalctl` commands.
- A supervisor of two areas can move a robot between them, so the other supervisors of the target area gain it.
- A platform admin's mistake adding a member is the residual risk; it is audited.
- Reference: [Area supervisors](../reference/api.md#area-supervisors).
