# 0010. Áreas locales con supervisores asignados por admin

> English version: [docs/decisions/0010-local-areas-with-admin-assigned-supervisors.md](../../decisions/0010-local-areas-with-admin-assigned-supervisors.md)

- Estado: Aceptada
- Fecha: 2026-10-17

## Contexto y problema

La baja (offboarding) de la automatización de una persona (sus tokens y robots) no debería esperar a un admin de la plataforma, pero tampoco debe dar a nadie control sobre el acceso ni sobre sus pares.

## Opciones consideradas

- Reutilizar grupos: descartada, agregar a alguien a un grupo de acceso nunca debe ponerlo bajo un supervisor (privilegio por efecto colateral).
- Una cadena de managers leída del IdP: no elegida.
- Reutilizar el manager de la iniciativa: descartada, los managers administran el acceso, los supervisores administran las credenciales de las personas.
- Un contenedor definido en el portal, como las unidades administrativas de Entra, los resource sets de Okta o los roles con alcance por OU de Workspace (elegida).

## Resultado de la decisión

Las áreas, `area_members` (un área por persona, solo humanos) y `area_supervisors` son tablas propias, gestionadas solo por admins de la plataforma. Una persona es supervisable cuando es miembro humano de un área, no es admin de la plataforma y no supervisa ningún área, de modo que los supervisores nunca alcanzan a sí mismos, entre sí ni a los admins. Un supervisor puede listar metadatos, revocar tokens y cambiar el `owner_user_id` de un robot dentro de sus áreas, nunca grants, grupos ni acceso. La capacidad es solo de sesión de navegador, se evalúa desde la base de datos en cada request, y los objetivos fuera de alcance responden 404.

## Consecuencias

- Sin jerarquía, sin renombrado, sin vista de auditoría para supervisores ni notificaciones en este diseño.
- Más adelante, no diseñado: sincronización de miembros desde el IdP (`area_members.source`, por `orgUnitPath` de Workspace o un grupo de Google), jerarquía de áreas, renombrado, una vista de auditoría para supervisores, notificaciones y comandos de `portalctl`.
- Un supervisor de dos áreas puede mover un robot entre ellas, por lo que los otros supervisores del área destino lo ganan.
- Un error de un admin de la plataforma al agregar un miembro es el riesgo residual; queda auditado.
- Referencia: [Supervisores de área](../reference/api.md#supervisores-de-área).
