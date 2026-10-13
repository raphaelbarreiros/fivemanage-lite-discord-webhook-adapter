# Patch mínimo de confirmação de ingestão

Base: `74ba491368d88d2747d9013cfd3fc4bcd78d1e34` de https://github.com/fivemanage/lite.
Arquivo: `0001-confirm-ingestion.patch`. Licença do Lite preservada em `LICENSE.md` (FSL-1.1-ALv2); não MIT.

## Problema comprovado

Na base original, `Service.SubmitLogs` retornava `void`, registrava falhas de dataset/ClickHouse e o handler sempre respondia `200 {"status":"ok","data":"ok"}`. O teste `integration/probe_original.py` obteve 200 no caso normal, em dataset inexistente e com o ClickHouse realmente parado. Somente o normal apareceu na consulta por API. O patch não escreve diretamente em tabelas nem introduz outra fila.

## Alterações

1. `SubmitLogs` retorna `error`; dataset inexistente usa sentinel baseada em `sql.ErrNoRows`. Falha de geração de identificador aborta o lote, em vez de enviar silenciosamente um lote parcial.
2. O handler responde 400 para dataset inexistente e 503 para falha de armazenamento/infraestrutura, sem incluir erros internos na resposta. Só retorna 200 depois de `BatchWriteLogRows` terminar sem erro.
3. O cliente ClickHouse recusa conexão desabilitada/nula, força `async_insert=0` e `wait_for_async_insert=1` por chamada e aborta/libera o batch em caminhos de erro. Removidos os prints de erros de batch potencialmente sensíveis.
4. Somente a resposta de sucesso contém `X-Fivemanage-Ingest: clickhouse-sync-v1`. O adaptador exige esse header **e** status HTTP 200 **e** JSON `status=ok,data=ok`. Não aceita um 200 upstream sem patch. O header identifica um contrato testado; não é prova criptográfica contra um servidor ou proxy malicioso.

Confirmação significa que o `Send()` síncrono do driver ClickHouse retornou sucesso no servidor de destino. Não significa replicação, backup, sobrevivência à perda do disco remoto ou transação distribuída com SQLite. Se a resposta se perder, haverá retry e poderá haver duplicação.

## Aplicação e teste sem produção

`integration/prepare.py` obtém o checkout exato em `.cache/lite`; o Dockerfile aplica o patch somente na camada de build. O checkout de referência permanece sem o patch. O build também compila o frontend oficial e inclui as migrações ClickHouse no caminho que o `runtime.Caller` upstream espera com `-trimpath`.

```sh
python3 integration/prepare.py
# Opcional: reproduzir primeiro o defeito original, sempre na stack descartável.
LITE_VARIANT=original LITE_PATCHED=0 docker compose up -d --build --wait lite
python3 integration/probe_original.py
# Contrato corrigido:
docker compose up -d --build --wait lite
go build -o .local/adapter ./cmd/adapter
python3 integration/e2e.py
```

Não execute `probe_original.py` contra a variante corrigida. Pare o serviço `adapter` do Compose antes de executar os testes nativos, que usam as mesmas portas locais. O script E2E muda somente containers deste projeto Compose e restaura Lite/ClickHouse nos blocos `finally`.

## Resultados observados

- Corrigido: ingestão 200 com confirmação, dataset inexistente 400 sem confirmação, ClickHouse parado 503 sem confirmação.
- Após volta do ClickHouse, pendências entregues e removidas; evento consultável pela API do Lite.
- Payload Discord decodificado de base64 idêntico aos 1.063 bytes de entrada, incluindo Unicode, espaços, campos repetidos, propriedades desconhecidas e inteiros grandes.
- `SIGKILL` antes de encaminhar ao Lite: uma cópia remota após retomada. `SIGKILL` depois da gravação remota mas antes da confirmação local: duas cópias, mesmo identificador interno.

O patch não altera a transformação de metadata, retenção de 30 dias codificada em `SubmitLogs`, autenticação, CORS, cache de token ou o limite upstream de 20 requisições/s por IP. Esses comportamentos devem ser considerados ao atualizar ou expor o Lite. A configuração de retenção do dataset não é aplicada por esse método na base testada.
