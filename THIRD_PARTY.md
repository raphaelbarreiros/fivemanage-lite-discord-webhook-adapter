# Licenças e proveniência

O código original do adaptador é MIT (`LICENSE`). Isso **não** relicencia o Lite nem suas alterações.

## SQLite e Go

- `modernc.org/sqlite v1.60.1`: BSD-3-Clause; motor **SQLite 3.53.4**, confirmado por `SELECT sqlite_version()` em macOS/arm64 e exigido pelo startup. Suporta também Linux/arm64 e Linux/amd64 sem CGo. `modernc.org/libc v1.77.1` permanece na versão exigida pelo driver.
- SQLite original: domínio público. O motor 3.53.4 inclui a correção do [WAL-reset corruption bug](https://sqlite.org/wal.html#walresetbug), incorporada em 3.53.0 e em 3.51.3; não foi selecionado apenas pela data do driver. [Histórico oficial](https://sqlite.org/changes.html).
- Go **1.27.2**, versão estável obtida pelo toolchain oficial e fixada no módulo e nas imagens. A ferramenta instalada inicialmente era 1.26.1; não foi substituída globalmente.
- Avisos das dez dependências efetivamente incluídas no binário estão em `licenses/`, incluindo avisos agregados de código traduzido. Versões/checksums estão em `go.mod`/`go.sum`. O build da imagem os distribui junto do executável.
- Não há licença comercial obrigatória nessas dependências para este uso. Consulte os textos integrais, não esta síntese, para redistribuição.

## Fivemanage Lite

Base testada: [`74ba491368d88d2747d9013cfd3fc4bcd78d1e34`](https://github.com/fivemanage/lite/tree/74ba491368d88d2747d9013cfd3fc4bcd78d1e34), obtida da branch `develop`. Não é uma alegação de release estável ou de segurança de todas as dependências upstream.

O arquivo [`LICENSE.md`](https://github.com/fivemanage/lite/blob/74ba491368d88d2747d9013cfd3fc4bcd78d1e34/LICENSE.md) é **Functional Source License 1.1, ALv2 Future License (FSL-1.1-ALv2)**. O README da base diz MIT, mas não corresponde ao arquivo da licença. A FSL permite explicitamente uso interno; restringe usos comerciais concorrentes e concede Apache-2.0 no segundo aniversário da disponibilização da versão. Não tratar essa versão como MIT nem como Apache-2.0 já efetiva sem verificar a data aplicável.

Patch e cópia integral dos termos em `patches/lite/`. Copyright Fivemanage AS preservado. O Dockerfile do Lite inclui os termos na imagem e aplica o patch somente durante seu build. Não se escreve diretamente em tabelas internas do Lite/ClickHouse para ingerir eventos.

## Imagens de integração

Imagens por versão e digest no Compose/Dockerfiles: Go 1.27.2-bookworm, Alpine 3.22.2, PostgreSQL 17.4-bookworm, ClickHouse 25.3.3.42 e Node 22.14.0-bookworm-slim (somente build do frontend oficial, pnpm 10.13.1 com lockfile upstream). As respectivas imagens e dependências conservam seus próprios avisos/licenças. Fixar digest reproduz a base testada; **não** significa atualização automática ou ausência de vulnerabilidades. Revalidar patches de segurança antes de adoção fora do laboratório.
