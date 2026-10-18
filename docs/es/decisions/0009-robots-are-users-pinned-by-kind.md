# 0009. Los robots son usuarios fijados por tipo

> English version: [docs/decisions/0009-robots-are-users-pinned-by-kind.md](../../decisions/0009-robots-are-users-pinned-by-kind.md)

- Estado: Aceptada
- Fecha: 2026-10-16

## Contexto y problema

La automatización necesita una identidad que pueda tener grants (asignaciones de rol) y aparecer en la auditoría y en el `sub` de la aserción, sin ser una persona, sin poder ser encontrada por un login de Google y sin convertirse en admin de la plataforma.

## Resultado de la decisión

Los robots son filas de `users` con `kind = 'robot'`, de modo que todo lo indexado por id de usuario funciona sin cambios (grants, `access.Decide`, `adminz`, el actor de auditoría, el `sub` de la aserción).

`users.kind` es `human` o `robot`; `google_sub` es NOT NULL si y solo si el tipo es human, por lo que un login de Google nunca puede resolverse a un robot. `platform_admins` y `group_members` fijan `kind = 'human'` mediante una FK compuesta a `users (id, kind)`, por lo que ninguna puede referenciar a un robot. Los robots obtienen acceso solo mediante grants directos, nunca mediante grupos. Un robot puede tener `manages_access` pero nunca es un manager efectivo, de modo que cada iniciativa conserva un manager humano. El dueño (`owner_user_id`) es informativo, no autoridad; un dueño que se va nunca deshabilita automáticamente sus robots, y la baja (offboarding) revisa la columna de dueño.

## Consecuencias

- `/users/*` y `/robots/*` se filtran por `kind` en SQL y responden 404 para la otra familia.
- Un robot sigue funcionando cuando su dueño se deshabilita, hasta que alguien lo deshabilite o revoque sus tokens.
- Referencia: [Humanos y robots](../reference/data-model.md#humanos-y-robots).
