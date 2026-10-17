# 0011. ON DELETE RESTRICT for area deletion

- Status: Accepted
- Date: 2026-10-17

## Context and Problem

An area must not be deleted while it has members or supervisors. The portal role holds no `UPDATE` on `areas`, so it cannot lock the row before counting.

## Considered Options

- Lock the area row and count first: needs `UPDATE` on `areas`, which the portal role does not hold.
- Rely on `NO ACTION` foreign keys: raises `23503`, the same code as an insert that references a missing area.
- `ON DELETE RESTRICT` and a plain `DELETE` (chosen).

## Decision Outcome

The foreign keys of `area_members` and `area_supervisors` to `areas` use `ON DELETE RESTRICT`. `RESTRICT` raises `23001` (`restrict_violation`), not the `23503` of `NO ACTION` and of the inserts. The portal repository maps `23001` on `area_members_area_id_fkey` or `area_supervisors_area_id_fkey` to 409 on delete (`portal.ErrInUse`), and `23503` on the same constraints to 404 when adding a member or supervisor to an area deleted concurrently. All four foreign keys of the two tables join `referenceFields`.

## Consequences

- The delete takes no lock and needs no extra privilege; both orders of the race are tested.
- A new foreign key to `areas` must be added to the mapping.
- Reference: [Area API](../reference/api.md#area-api).
