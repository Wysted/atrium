# 0004. Login solo con Google con allowlist de hd

> English version: [docs/decisions/0004-google-only-login-with-hd-allowlist.md](../../decisions/0004-google-only-login-with-hd-allowlist.md)

- Estado: Aceptada
- Fecha: 2026-10-14

## Contexto y problema

Las empresas que adoptan atrium usan Google Workspace o Cloud Identity. El login debe vincular el acceso a una identidad estable, funcionar sin guardar credenciales de Google y cortar el acceso a usuarios que se fueron, aunque Google no envía eventos RISC para usuarios de Workspace.

## Opciones consideradas

- Google OIDC (authorization code + PKCE) con la pantalla de consentimiento en Internal (elegida).
- Identidad por email: descartada, el email no es un identificador.
- Pre-provisionar usuarios por email: descartada, el acceso se vincula a `google_sub`, que Google nunca reasigna.
- Sincronizar las suspensiones de Workspace mediante la Admin SDK Directory API: más adelante, se sigue en [TODO.md](../../../TODO.md).

## Resultado de la decisión

Authorization Code + PKCE (S256) + `state` + `nonce`; el claim `hd` se envía solo como pista de UI y se hace cumplir contra `PLATFORM_ALLOWED_HD` sobre el ID token (las cuentas personales no llevan `hd`). La identidad es `sub`, mapeado a `users.google_sub`; los usuarios se crean just in time en el primer login. No se guarda ningún token de Google. El emisor está fijo en `https://accounts.google.com` y nunca es configurable. La revocación es local: un flag `disabled`, sesiones eliminadas al deshabilitar, y la caché de decisiones de 30 s acota el corte.

## Consecuencias

- Google es hoy el único proveedor de identidad soportado.
- Los usuarios deben iniciar sesión una vez (o recibir acceso mediante un grupo) antes de los grants directos.
- Un usuario suspendido en Google pero no deshabilitado en el portal conserva el acceso hasta el límite de sesión de 8 h, y mantiene tokens funcionando hasta el límite personal.
- Referencia: [Login con Google y sesiones](../reference/login.md).
