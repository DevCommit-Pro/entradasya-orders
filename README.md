# Orders — Go

Orquestador local en memoria del contrato API §§8, 20 y 21. Usa HTTP/JSON para events, payments y tickets; no lee datos internos de otros servicios. Sin DB, colas ni persistencia.

## Arrancar

```sh
cp .env.example .env
# Editar JWT_SECRET e INTERNAL_API_KEY para que coincidan con los otros servicios.
set -a
source .env
set +a
go run ./cmd/api
```

Escucha en 127.0.0.1:4003. Arrancar las versiones actualizadas de events/payments/tickets; auth se necesita para obtener JWT, pero orders no lo llama al validar cada petición.

Config: PORT 4003; JWT_SECRET e INTERNAL_API_KEY obligatorios (mínimo 32 bytes); EVENTS_URL/PAYMENTS_URL/TICKETS_URL obligatorios (orígenes HTTP(S), sin path/query/credenciales); NOTIFICATIONS_URL opcional; HTTP_TIMEOUT_MS 2000 (máximo 3000), OPERATION_TIMEOUT_MS 6000 (máximo 6000), RECOVERY_INTERVAL_MS 1000. Go no carga .env automáticamente.

No se requieren descargas nuevas: JWT usa golang-jwt/v5 previamente preparado; resto biblioteca estándar. Conservar go.sum generado.

## Rutas

| Método | Ruta | Acceso |
|---|---|---|
| GET | /health | Estado local, dependencias no comprobadas |
| POST | /orders | customer, JWT e Idempotency-Key obligatorios |
| GET | /orders/mine | Autenticado; solo propias, fecha descendente |
| GET | /orders/{id} | Autenticado propietario; ajeno/inexistente 404 |
| GET | /orders/stats | organizer; array por cada evento propio, incluso sin ventas |
| GET | /orders/stats?eventId=... | organizer dueño; objeto SalesStats |

Sin prefijo /api en el servicio. El Location de una respuesta pendiente conserva `/api/orders/{id}` del contrato público; gateway sigue pendiente. GET no reinicia operaciones ni cobra: únicamente lee estados.

Compra:

```json
{"eventId":"e-tormenta-andina","sectorId":"s-ta-gen","quantity":2,"card":{"number":"4242424242424242","holder":"Camila Rojas","expiry":"08/28","cvc":"123"}}
```

Solo tarjetas ficticias. Datos del comprador provienen del JWT; precio y snapshot del evento/sector se congelan en la reserva autoritativa de events. No se aceptan precios ni userId arbitrarios en el body. Cantidad entre 1 y 6, JSON entero. Máximo body 64 KiB.

## Flujo y respuestas

Validar → consultar evento/sector publicado y futuro → crear pending → reservar y cotizar → cobrar → confirmar cupo → emitir tickets → intentar correo si fue configurado.

- Nueva orden terminada: 201, incluida rejected/sold_out o rejected/payment_declined.
- Misma clave y mismo payload recuperado: 200 cuando finalizó.
- Sigue procesando o requiere recuperación: 202 con Order completo y Location.
- Misma clave con otro payload: 409 idempotency_conflict.
- La clave está delimitada por usuario. Se registra antes del preflight, evitando órdenes duplicadas bajo concurrencia.

Cada Order incluye paymentStatus/ticketIssuanceStatus/processingStatus y ticketIds siempre como array. paid solo significa pago aprobado; las entradas se muestran cuando emisión está issued. Email false nunca revierte compra. Pendientes/fallos técnicos no se presentan como pago rechazado.

Los importes se parsean/calculan en centavos exactos; JSON numérico en Bs. No se usan floats para dinero. Reportes agregan solo órdenes paid con importes históricos, no precios actuales multiplicados por ventas. Capacidad y metadata actuales se obtienen de events. byDay contiene 14 días, según UTC−4 de Bolivia y fecha de creación de la orden.

## Timeouts y recuperación

El POST espera como máximo el presupuesto configurado, hasta seis segundos; llamadas HTTP tienen timeout adicional y respetan el contexto. El procesamiento no depende de que el navegador siga conectado. Worker local revisa recuperaciones; no es una cola persistente.

- Reserva incierta: repetir reserve con el mismo orderId recupera cotización sin duplicar cupos.
- Pago incierto: consultar payments/by-order. Un 404 aislado no es prueba de pago rechazado y no libera cupos.
- Pago aprobado/confirmación pendiente: repetir confirm idempotente antes de emitir.
- Emisión fallida o respuesta perdida: repetir issue con el mismo payload/orderId conserva entradas.
- Pago declined/liberación fallida: seguir rejected con recovery_required hasta confirmar release. No decir “liberadas” antes.
- No hay PAN/CVC en el repositorio. Existen transitoriamente durante el intento acotado; se guarda fingerprint HMAC con clave aleatoria del proceso y cardLast4 copiado. No se promete borrado seguro de memoria administrada.

**Límite deliberado:** si nunca se pudo iniciar el pago y el intento ya no conserva la tarjeta, la recuperación automática no inventa una tarjeta ni un cobro. Conserva recovery_required. Repetir POST con la misma clave y payload mientras el cliente aún los tiene permite continuar. Si un pago incierto no aparece en lookup, solo ese reintento explícito puede volver a enviar charge idempotente para la MISMA orden. Un GET jamás lo hace. Tras perder clave/datos por recarga, no hay una política administrativa de cancelación/reembolso implementada: consultar historial y revisar la demo, no crear una nueva compra automáticamente. Reinicios independientes pueden requerir reset coordinado; sin garantías persistentes.

NOTIFICATIONS_URL vacío deja emailSent=false. Si se configura, solo 200 con sent=true confirma envío; 202/fallo/timeout no lo hacen. Intento acotado a un segundo y al presupuesto restante. Correo real, PDF/QR y checkin siguen pendientes; ticketIssuanceStatus=issued representa entradas lógicas, no PDF disponible.

## Datos iniciales y demo

Orders inicia vacío, igual que transacciones/tickets. Se retiró la reserva artificial seed-pending-voley para no dejar reservas sin orden real. Events empieza con cupos libres; para demostrar agotado mediante el flujo real, con servicios corriendo:

```sh
# Desde la raíz:
bash scripts/seed-demo-order.sh
```

Compra seis entradas de vóley con usuario semilla y una clave fija. Repetir es idempotente; si responde pending, consultar la misma orden sin cambiar clave. El script NO se ha ejecutado con procesos en red en el entorno restringido del agente.

## Pruebas

```sh
go test -race ./...
go vet ./...
```

Pruebas de servicio/handlers con dependencias simuladas y pruebas del cliente HTTP de producción mediante RoundTripper en memoria. No abren puertos y no equivalen al recorrido entre procesos ni frontend. Cubren compra, privacidad, JWT/ownership, concurrencia, límites de tiempo, pérdida de respuestas, compensación y reportes históricos.
