# Handoff

## Entrega

Repositório local independente: `/Users/raphaelbarreiros/Workspaces/fivemanage-lite-discord-webhook-adapter`.

Adaptador funcional Go/net/http + SQLite embarcado: autenticação por origem, JSON Discord dentro da matriz, commit antes de 204, um worker, retry/TTL/cotas/descarte, manutenção incremental/WAL, métricas/health e shutdown. Payload original em uma cópia pendente; raw base64 reversível no Lite; sem histórico de entregues no adaptador. Dockerfile/Compose completos, fixtures sintéticas, testes Go e scripts de integração.

Patch separado em `patches/lite/`: corrige falso 200 da ingestão. Base Lite `74ba491368d88d2747d9013cfd3fc4bcd78d1e34`; licença FSL-1.1-ALv2 permite uso interno e não é MIT. Adaptador original MIT. Go 1.27.2, modernc SQLite 1.60.1/motor 3.53.4 (correção WAL-reset incluída).

## Retomar o laboratório já preparado

```sh
docker compose up -d --wait
python3 integration/compose_smoke.py
```

A stack está **rodando localmente** após a correção do marcador ativo apontada pelo advisor e novo smoke ponta a ponta. Volumes, configuração e secrets sintéticos privados foram conservados. Não execute bootstrap novamente para retomar esse laboratório. Para instalação local do zero, seguir o README. Nada foi publicado, commitado, enviado por push, implantado em produção ou alterado nos scripts FiveM reais.

## Evidência

- `go vet`, `go test -race`, `go mod verify`, builds nativo e Docker: passaram.
- Lite real: original responde 200 em dataset inválido/storage parado; patch responde 400/503 e só confirma sucesso efetivo.
- Round-trip exato de 1.063 bytes, IDs grandes, campos repetidos e Unicode.
- SIGKILL antes/depois do envio, reinício, indisponibilidade Lite/ClickHouse, SQLITE_FULL, lock, overflow/TTL, checkpoint ocupado e retomada: exercitados.
- Duplicação esperada comprovada: duas cópias remotas com mesmo ID na janela de queda após gravação.
- Carga limitada: 200/200 aceitos e consultados no Lite; p95 de admissão 4,387 ms; drenagem em 24,474 s. Não extrapolar para produção.
- Pico de SQLite: 6.225.920 bytes; depois de exclusão/manutenção, 24.576 bytes e WAL zero. Reutilização de páginas comprovada.
- Busca das credenciais sintéticas conhecidas nas saídas/fontes/logs: limpa.

Detalhes, comandos, ambiente e limitações em `TEST_RESULTS.md`; operação, diagnóstico, backup, configuração e matriz em `README.md`.

## Riscos e próximos passos

204 é aceitação local, não entrega final. Há perdas por rejeição/expiração/descarte/perda de volume e duplicação na janela remota/local. Uma instância, volume local, sem HA/exactly-once. Lite sem patch não serve como confirmação confiável. Imagens fixadas não significam ausência de vulnerabilidades.

**Auto-revisão**, não revisão independente. Não foram testados falha elétrica, restauração operacional de backup, Linux/amd64 runtime, soak prolongado ou scripts reais. Sem bloqueio conhecido nos fluxos locais exercitados; não é declaração de produção pronta.

Antes de adotar: revisão independente, revisão de atualizações de segurança e licença do Lite, definição de buffer/TTL, TLS e redação no proxy, alertas e ensaio autorizado com um script real. Deploy/troca de URLs reais dependem de autorização do usuário.

## Multi-organization update

Each source now requires `lite_token_file`; the former global setting is rejected. The Lite token chooses that source's organization, and `dataset` is resolved inside it. Multiple organizations share one adapter instance, worker, HTTP transport, and SQLite queue. See the English README and two-source `config.example.json` for configuration and Compose secret mounts.

Real Lite isolation was verified with identically named datasets in two organizations, interleaved queued events, an unavailable destination, and SIGKILL/restart. Counts were alpha=2 and beta=1, with stable event IDs and exact payload recovery. Tests with the race detector, vet, the existing E2E, and the rebuilt container smoke passed. The local demo is running with the migrated configuration; no production configuration was changed.

Migration: move the old token path into every existing source without changing its destination, remove the global key, and rebuild/recreate the adapter. Do not reassign an existing source to a different organization while its queue contains pending events. Quotas, pacing, and backoff remain shared across organizations.
