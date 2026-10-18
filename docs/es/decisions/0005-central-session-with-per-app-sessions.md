# 0005. Sesión central con sesiones por app

> English version: [docs/decisions/0005-central-session-with-per-app-sessions.md](../../decisions/0005-central-session-with-per-app-sessions.md)

- Estado: Aceptada
- Fecha: 2026-10-14

## Contexto y problema

Las apps viven en distintos hosts bajo un mismo dominio padre. El login ocurre una vez en el host de autenticación, pero cada host de app necesita su propia credencial, y el traspaso entre hosts debe resistir el login CSRF y la fijación de sesión.

## Opciones consideradas

- Una sesión central en el host de autenticación más una sesión de app por host de app, vinculadas por códigos de login de un solo uso (elegida).
- `SameSite=Strict`: rompería el retorno desde Google y los enlaces desde correo o chat.

## Resultado de la decisión

Los valores de las cookies son aleatorios de 256 bits y solo se guarda su SHA-256. Todas las cookies son host-only (`HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, sin `Domain`) y usan el prefijo `__Host-Http-` (draft-ietf-httpbis-layered-cookies; Chrome 140+, Firefox 143+; los navegadores sin soporte igual aplican las reglas de `__Host-`, por lo que nunca es más débil). Un `login_code` (30 s, un solo uso, consumo atómico) lo acepta únicamente el navegador que posee la cookie `__Host-Http-platform_bind` correspondiente. Se emite un nuevo id de sesión en cada login. La revocación es eliminación; las sesiones de app caen en cascada desde la central. El logout muestra una página de "signed out" en lugar de redirigir al login, porque Google mantiene su propia sesión y volvería a iniciar la sesión del usuario en silencio. Los cambios de IP y User-Agent se registran y alertan, nunca se usan para cortar la sesión (NAT y redes móviles).

## Consecuencias

- Un usuario deshabilitado pierde el acceso dentro de la caché de decisiones de 30 s; una sesión dura como máximo 8 h más los límites de inactividad.
- Rate limits en `/login` y `/oauth2/callback`; los flujos, códigos y sesiones expirados los purga un job.
- Referencia: [Login con Google y sesiones](../reference/login.md#reglas).
