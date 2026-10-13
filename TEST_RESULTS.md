# Resultados reais da validação

Validação local de 13/10/2026. **Auto-revisão e execução pelo implementador**; não houve revisão independente nem auditoria externa. Nenhuma produção, script real, push ou deploy foi acessado/executado. Todos os payloads e credenciais são sintéticos/descartáveis.

## Ambiente e versões

- Host: macOS 27.0, arm64, Apple M3 Max, 14 CPUs lógicas, 38.654.705.664 bytes de RAM (36 GiB).
- Docker Desktop 4.71.0, Engine 29.4.1, Compose 5.1.3; VM Linux/aarch64, 14 CPUs e 13.582.884.864 bytes de RAM disponíveis à VM.
- Go 1.27.2; `modernc.org/sqlite v1.60.1`, `modernc.org/libc v1.77.1`, SQLite efetivo **3.53.4**. `go mod verify`: todos os módulos verificados.
- Lite base `74ba491368d88d2747d9013cfd3fc4bcd78d1e34`, comparado sem patch e com `patches/lite/0001-confirm-ingestion.patch`.
- PostgreSQL 17.4-bookworm; ClickHouse 25.3.3.42. Imagens por digest no Compose. Node 22.14.0-bookworm-slim/pnpm 10.13.1 usados para compilar o frontend oficial. Não foi feita avaliação visual desse frontend, que não foi alterado.
- Adaptador executado nativamente em macOS/arm64 e em container Linux/arm64; Go race detector executado no host. Não foi executado em Linux/amd64/Windows.

## Contrato real de ingestão

Fonte: `integration/probe_original.py`, `integration/e2e.py` e consultas HTTP à API do Lite, não inserções diretas em tabelas.

| Cenário | Lite original | Lite com patch |
|---|---|---|
| Ingestão normal | HTTP 200; evento encontrado | HTTP 200 + header de confirmação; evento encontrado |
| Dataset inexistente | HTTP 200, sem gravar | HTTP 400, sem confirmação |
| ClickHouse realmente parado | HTTP 200, sem gravar | HTTP 503, sem confirmação |
| Após recuperação de armazenamento | Não é usado como contrato confiável | Pendência entregue, encontrada na API e removida do SQLite |

Na prova original houve uma linha antes e uma depois dos dois envios inválidos: ambos retornaram sucesso falso. A metadata `9007199254740993123` chegou como `9007199254740993000`; `"  a   b\n c  "` virou `"a b c"`; booleano e null desapareceram; objeto foi achatado. Esses comportamentos motivam `discord_payload_b64`, não um pressuposto sobre preservação upstream.

Com patch, os **1.063 bytes** da fixture voltaram idênticos após consulta pela API e decode base64. Verificados origem, `_resource` confiável, IDs grandes de personagem/Discord, sessão distinta, quantidade normalizada pela regra, repetição/ordem dos campos, Unicode, propriedades desconhecidas e timestamp declarado separado do recebimento.

## Entrada, fila e falhas

| Critério | Execução/evidência |
|---|---|
| Texto simples, múltiplos embeds, campos repetidos, Unicode, desconhecidos, IDs grandes | Testes `internal/event` e round-trip no Lite real |
| Autenticação inválida, JSON inválido, corpo excessivo | HTTP 401/400/413 nos testes do handler com SQLite em arquivo |
| wait=true, query extra/duplicada, multipart, attachments, métodos/subrotas | Rejeições explícitas 400/415/405/404; sem enfileirar |
| Commit antes do 204 | `ResponseWriter` observa uma conexão SQLite independente **no instante de WriteHeader(204)**; não só depois do retorno do handler |
| Concorrência de entrada/limpeza | 8 goroutines × 50 inserts/deletes, concorrendo com 20 manutenções; fila final vazia, race detector limpo |
| Configuração efetiva | Startup verifica WAL, FULL, incremental vacuum, busy timeout, foreign_keys e motor 3.53.4 na conexão dedicada; startup do container também observado |
| Reinício abrupto | Subprocesso sai sem fechar SQLite; WAL existente recuperado, mesmo ID/payload, agendamento retomado. E2E usa SIGKILL real nos subprocessos que criou |
| Lock local | Segundo escritor segura `BEGIN IMMEDIATE`; request retorna 503 em tempo limitado, depois volta a aceitar 204 ao liberar lock |
| Falha real de escrita SQLite | `max_page_count` induz **SQLITE_FULL (13)** em arquivo real; rollback sem aceitação, recuperação após liberar limite e `integrity_check=ok` |
| Checkpoint ocupado | Leitor mantém snapshot; checkpoint retorna busy rapidamente e admissão seguinte funciona; depois da liberação o truncamento termina |
| Limites e descarte | `reject_new`, reserva física, `drop_oldest` protegendo evento em envio, expiração, limite de tentativas e erro permanente exercitados |
| TTL após retries | Expira pelo recebimento original mesmo após reagendamento para o futuro |
| Lite indisponível | Container Lite parado: aceita localmente, mantém/reagenda, reinicia processo e entrega após recuperação |
| Armazenamento remoto indisponível | ClickHouse parado: 503 real, pendência preservada; tentativas observadas limitadas, sem loop agressivo; recuperação e consulta confirmadas |
| Queda antes de envio | Proxy local segura antes de encaminhar; SIGKILL; retomada gera **1 cópia** no Lite |
| Queda após gravação remota | Proxy recebe sucesso real do Lite e segura confirmação local; SIGKILL; retomada gera **2 cópias com o mesmo adapter_event_id** |
| Erro depois de confirmação | Trigger de teste falha o DELETE local; registro conservado e reenviado com ID estável; remoção após recuperação |
| Shutdown | SIGTERM após aceitação; reinício e drenagem, sem apagar evento não confirmado |
| Contenção de instância | Segunda abertura do mesmo diretório recusada pelo lock exclusivo |
| HTTP redirecionado | Destino de redirect não recebe chamada/credenciais; 200 sem contrato confirmado não conta como entrega |

Os testes de falha não encheram o disco do host, não interromperam outros projetos e não simularam queda elétrica. `SQLITE_FULL` por limite de páginas não comprova todas as falhas de filesystem/SSD. Nenhum desses testes demonstra HA, sobrevivência à perda do volume ou exatamente uma entrega.

## Remoção lógica, reutilização e redução física

`TestReusesPagesAndShrinksAfterPeak`: 100 mensagens de 61.440 bytes, arquivo persistente, página de 4.096 bytes. Medidas separadas de `.db` e WAL:

| Momento | Arquivo principal (bytes) | WAL (bytes) | Páginas livres |
|---|---:|---:|---:|
| Pico após checkpoint | 6.225.920 | 0 | 0 |
| Após DELETEs, antes da manutenção | 6.225.920 | 3.069.432 | 1.513 |
| Fila vazia após recuperação incremental | 24.576 | 0 | 0 |

A segunda inserção do mesmo pico reutilizou as páginas: **1.520 páginas**, sem crescimento significativo. Não foi usado VACUUM completo nem apagado WAL/SHM manualmente. O arquivo final não tem tamanho zero. Essas medidas de um ensaio não são uma quota física prometida.

## Carga sintética limitada

`python3 integration/load.py`: adaptador **nativo** no host, fila no filesystem local, Lite/PostgreSQL/ClickHouse no Docker. Uma rodada, 200 eventos, 8 emissores, JSON de 953 bytes, um worker com intervalo de 100 ms mais HTTP. Não foi medida carga de produção nem realizado soak test.

| Medida | Resultado |
|---|---:|
| Requisições / HTTP 204 | 200 / 200 |
| Duração da rajada de admissão | 0,065 s |
| Taxa de admissão local observada | 3.083,6 eventos/s |
| Latência de admissão p50 | 2,072 ms |
| Latência de admissão p95 | 4,387 ms |
| Latência de admissão máxima | 5,599 ms |
| Pendências após a rajada | 200 |
| Tempo total até drenagem | 24,474 s |
| Logs confirmados por contagem da API Lite | 200 |

A taxa de admissão mede commit no buffer em uma rajada curta; **não é a taxa de entrega sustentada**. O worker é deliberadamente limitado, e o rate limiter do Lite foi conservado. Não extrapolar esses números para FiveM real, hardware diferente, payloads maiores, disco lento, outros concorrentes ou indisponibilidade longa.

## Ferramentas e smoke executados

- `gofmt -w cmd internal` e formatação dos três arquivos alterados no patch Lite.
- `go vet ./...`: passou para o adaptador.
- `go test -race ./...`: passed for event interpretation, HTTP/worker, outbound client, configuration, and queue packages. Configuration regressions cover explicit source credentials, rejection of the removed global setting, and missing token files. `cmd/adapter` startup is exercised by native subprocesses and containers.
- `go mod verify`: todos os módulos verificados.
- `go build -o .local/adapter ./cmd/adapter`: passou.
- Builds Docker do Lite original, Lite corrigido e adaptador Linux/arm64; healthchecks de PostgreSQL, ClickHouse, Lite e adaptador passaram.
- `integration/compose_smoke.py`: novo evento aceito com 204, encontrado na API real, raw payload idêntico, fila vazia e readiness 200. Reexecução identifica um novo ID para não confundir fixture anterior com entrega nova.
- `integration/scan_secrets.py`: sem correspondência das credenciais descartáveis conhecidas nem URLs Discord reais em fontes/evidências/logs examinados. Uma captura de logs dos containers com 35.251 bytes também não continha valores dos secrets atuais. A varredura não garante ausência de segredos desconhecidos.

Arquivos de evidência de máquina ficam localmente em `artifacts/original-lite.json`, `e2e.json`, `load.json`, `load.metrics`, `compose.metrics`. São dados de **testes sintéticos**, não um arquivo histórico do adaptador. Saídas operacionais nativas ficam em `.local/*/adapter.log`, sem payload/token.

## Ajustes encontrados durante a execução

- Primeiro build runtime do Lite não tinha as migrações ClickHouse no caminho esperado por `runtime.Caller` com `-trimpath`; a imagem foi corrigida e iniciou com migrações reais, sem alterar esse código upstream.
- Auto-revisão identificou denominador incorreto da latência quando HTTP remoto confirmava e DELETE local falhava. A asserção reproduziu a falha; contador de chamadas separado corrigiu o resultado e o teste passou.
- Repetir a fixture no smoke inicialmente podia encontrar uma entrega anterior antes de a nova fila drenar. O smoke agora exige ID novo e aguarda explicitamente a fila vazia; foi reexecutado com sucesso.

## Não exercitado / limites antes de adoção real

- Não foi feito teste de falha elétrica, perda/corrupção real do volume, backup/restauração operacional, hardware de produção, long soak ou Linux/amd64 runtime.
- Não foram auditados todos os componentes do Lite, sua UI, dependências, CORS/autorização do dashboard ou segurança dos serviços de banco. Imagens fixadas não equivalem a imagens certificadas/atualizadas para produção.
- Nenhuma compatibilidade fora da matriz foi prometida; não foram testados scripts reais que validam domínio Discord.
- Sem revisão independente. Antes de adoção: revisão externa, política de buffer/TTL escolhida pelo operador, atualização de segurança revalidada, TLS/redação no proxy, monitoramento e teste controlado com um script autorizado. Não há bloqueio conhecido para os fluxos locais exercitados; isso não declara produção pronta.

## Correção após observação do advisor

Reproduzido `ErrBusy` permanente: a liberação anterior dependia do gate SQLite e podia expirar mantendo `active`. `Release(id)` agora usa marcador atômico com compare-and-swap, sem I/O, espera pelo gate ou contexto cancelável. Leituras do marcador também são atômicas; todos os callers foram migrados.

`TestReleaseWhileIOGateBlockedResumesDrain` falhou antes da correção e passou depois: mantém o gate ocupado além do prazo de admissão simulado, exige liberação enquanto o gate ainda está ocupado e depois reserva/conclui todas as pendências. `go test -race ./...` e `go vet ./...` passaram. Imagem reconstruída e smoke real no Compose passou (204, novo evento consultável no Lite, payload exato, fila vazia). O laboratório ficou rodando localmente; produção não foi alterada.

## Multi-organization delivery

Implemented required `sources[].lite_token_file`, removing the global credential setting without a fallback. The shared HTTP transport receives the trusted source token on each new request. One worker, one queue, and the existing SQLite schema are retained.

`integration/multi_org.py` passed against the real patched Lite: two organizations with the same dataset name `synthetic`, one native adapter process, and one persistent SQLite queue. Three interleaved events were accepted while Lite was unavailable. After SIGKILL and restart, organization alpha contained exactly its two events and organization beta exactly its one event. Payload-supplied organization, dataset, resource, and credential-path values could not change routing. Event IDs matched the pre-restart pending IDs, raw payloads round-tripped exactly, and credential scanning of the process log was clean.

`go test -race ./...`, `go vet ./...`, the existing `integration/e2e.py`, the rebuilt Docker image, and `integration/compose_smoke.py` passed after the migration. The default local Compose demo was re-provisioned with the per-source configuration and is running. Its default demo still uses one organization; the separate multi-organization scenario exercises two organizations. `artifacts/multi-org.json` contains the measured isolation result. Shared quotas and backoff remain instance-wide, not per-tenant scheduling or resource isolation.
