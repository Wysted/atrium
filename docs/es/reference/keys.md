# Llaves de firma, rotación, JWKS

> English version: [docs/reference/keys.md](../../reference/keys.md)

Cómo platform-api guarda, rota y publica las llaves ES256 que firman las aserciones. Fundamento: [ADR 0002](../decisions/0002-kek-wrapped-signing-keys-in-postgresql.md).

## Almacenamiento

- Las llaves ECDSA P-256 (ES256) viven en la tabla de PostgreSQL `signing_keys`: `kid`, JWK público, llave privada cifrada, estado, timestamps.
- Las llaves privadas usan cifrado de sobre (envelope encryption): cifradas con una key-encryption key (KEK) montada desde un Secret de Kubernetes; la KEK nunca llega a la base de datos.
- `kid` es la huella JWK de RFC 7638 (SHA-256 en base64url de la llave pública).

## Ciclo de vida

| Estado | Firma | En el JWKS | Duración |
|---|---|---|---|
| `next` | No | Sí | Creada en el bootstrap e inmediatamente después de cada promoción (por lo que normalmente está publicada ~30 días); promovida por rotación solo si lleva publicada al menos 1 h |
| `active` | Sí | Sí | 30 días |
| `retired` | No | Sí | 1 h (cubre TTL del token + tolerancia + caché del JWKS) |
| removed | No | No | Fila conservada para auditoría, material de llave privada eliminado |

- La rotación es automática; solo una réplica rota a la vez, protegida por un advisory lock de PostgreSQL.
- Rotación de emergencia (sospecha de compromiso): la llave se elimina del JWKS de inmediato y se promueve una nueva.
  Las apps pueden seguir aceptándola hasta que venza su caché de JWKS (máx. 5 minutos); reducir `max-age` acorta esa ventana.
  Si el JWKS es inalcanzable en ese momento, stale-if-error ([Comportamiento del SDK ante fallas](assertion.md#comportamiento-del-sdk-ante-fallas)) la extiende hasta 20 minutos (5 + 15),
  más hasta 30 s para una réplica que perdió la notificación de cambio.

## Reglas de falla y consistencia

- Siempre hay una llave `next`, creada inmediatamente después de cada promoción, de modo que una revocación de emergencia normalmente promueve una
  llave que las apps ya conocen. La revocación prefiere una `next` publicada durante al menos 1 h; durante la primera hora tras una
  promoción toma la más antigua, y si ninguna puede descifrarse activa una llave nueva. Solo si no existe ninguna `next` se activa una llave nueva directamente
  (las apps pagan entonces una nueva obtención del JWKS con rate limit, hasta 60 s de rechazos).
- Una llave se promueve solo después de que su llave privada se descifra con éxito dentro de la misma transacción.
- Si una réplica no puede cargar la llave activa actual, deja de firmar (fail closed) en lugar de conservar una antigua.
- Los timestamps de rotación vienen del `now()` de PostgreSQL, nunca de los relojes de las réplicas.
- Una réplica que perdió la notificación `signing_keys_changed` puede firmar con una llave revocada hasta por 30 s
  (recarga periódica); esto queda dentro de la ventana de emergencia de 5 minutos anterior.
- Las llaves privadas selladas llevan un byte de versión de formato, de modo que la KEK puede rotarse volviendo a sellar.
- Cada llave guarda la huella (`kek_id`) de la KEK que la selló: una derivación con separación de dominio, no reversible,
  de la KEK, nunca la KEK en sí. Una réplica cuya huella de KEK difiere de la de las llaves `active`/`next`
  informa `KEKMismatch`, falla la readiness, y se niega a rotar o revocar sin tocar ninguna fila.
  Solo con una huella coincidente un `next` que no se puede descifrar se trata como corrupto y se reemplaza automáticamente.

## Endpoint JWKS

- `GET /.well-known/jwks.json` con `Cache-Control: max-age=300`; solo llaves públicas.
- Las apps lo obtienen desde el listener del JWKS (`PLATFORM_JWKS_ADDR`) mediante la URL del servicio dentro del clúster, nunca mediante la ruta pública, y vuelven a obtenerlo ante un `kid` desconocido (con rate limit, ver [Comportamiento del SDK ante fallas](assertion.md#comportamiento-del-sdk-ante-fallas)).
- TLS interno (opcional, neutral respecto de proveedores): `PLATFORM_JWKS_TLS_CERT_FILE` y `PLATFORM_JWKS_TLS_KEY_FILE` (PEM; ambos o ninguno, de lo contrario es un error de configuración) hacen que el listener del JWKS sirva solo HTTPS, con TLS 1.3 como mínimo (todo cliente dentro del clúster, el SDK de Go y Grafana, lo habla). Los archivos pueden ser producidos por cualquier emisor (cert-manager, Vault agent, un Secret manual); el archivo de llave sigue la misma política de permisos que los demás archivos de secreto (sin escritura de grupo, nada para otros), mientras que el certificado es material público y se lee sin verificación de permisos (aun así un archivo regular, con límite de tamaño y validado como PEM). El par se vuelve a leer cada 30 s y se sustituye cuando su contenido cambió, de modo que los nuevos handshakes presentan el nuevo certificado sin reinicio. Un par inicial inválido detiene el arranque; un reemplazo inválido sigue sirviendo el último par bueno y se registra una vez por error distinto. El certificado debe llevar el SAN del nombre de servicio dentro del clúster que usan las apps. Los verificadores que usan una CA privada la toman de un bundle PEM: `PORTAL_JWKS_CA_FILE` (portal), `HELLO_JWKS_CA_FILE` (app de ejemplo), `Config.RootCAs` (SDK); sin él se aplican las raíces del sistema. Una URL de JWKS `http` registra una advertencia al arrancar el verificador.
