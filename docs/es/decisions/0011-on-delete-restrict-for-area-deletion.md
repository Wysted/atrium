# 0011. ON DELETE RESTRICT para la eliminación de áreas

> English version: [docs/decisions/0011-on-delete-restrict-for-area-deletion.md](../../decisions/0011-on-delete-restrict-for-area-deletion.md)

- Estado: Aceptada
- Fecha: 2026-10-17

## Contexto y problema

Un área no debe eliminarse mientras tenga miembros o supervisores. El rol del portal no tiene `UPDATE` sobre `areas`, por lo que no puede bloquear la fila antes de contar.

## Opciones consideradas

- Bloquear la fila del área y contar primero: requiere `UPDATE` sobre `areas`, que el rol del portal no tiene.
- Apoyarse en foreign keys `NO ACTION`: lanza `23503`, el mismo código que un insert que referencia un área inexistente.
- `ON DELETE RESTRICT` y un `DELETE` simple (elegida).

## Resultado de la decisión

Las foreign keys de `area_members` y `area_supervisors` hacia `areas` usan `ON DELETE RESTRICT`. `RESTRICT` lanza `23001` (`restrict_violation`), no el `23503` de `NO ACTION` y de los inserts. El repositorio del portal mapea `23001` sobre `area_members_area_id_fkey` o `area_supervisors_area_id_fkey` a 409 al eliminar (`portal.ErrInUse`), y `23503` sobre las mismas constraints a 404 al agregar un miembro o supervisor a un área eliminada concurrentemente. Las cuatro foreign keys de las dos tablas se suman a `referenceFields`.

## Consecuencias

- El delete no toma ningún lock y no necesita privilegios adicionales; ambos órdenes de la carrera están probados.
- Una nueva foreign key hacia `areas` debe agregarse al mapeo.
- Referencia: [API de áreas](../reference/api.md#api-de-áreas).
