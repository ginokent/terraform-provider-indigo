# Dependency requirements for building this provider

この provider を `go test ./...` / `go build ./...` まで通すには、以下の 2 点が必要です。

1. **Go module の取得許可**
   - `proxy.golang.org` への HTTPS アクセス（推奨）
   - もしくは GitHub への直接 HTTPS アクセス（`GOPROXY=direct` 利用時）
2. **HashiCorp モジュールの取得**
   - `github.com/hashicorp/terraform-plugin-sdk/v2`
   - `github.com/hashicorp/terraform-plugin-log`

## 推奨設定（社内プロキシがある場合）

```bash
go env -w GOPROXY=https://proxy.golang.org,direct
go env -w GOSUMDB=sum.golang.org
```

## 完全に外向き通信不可の環境での選択肢

- 接続可能な環境で `go mod vendor` を実施し、`vendor/` を同梱してからビルド
- あるいは社内の Go module mirror を用意し、`GOPROXY` をそこへ向ける

## 疎通確認コマンド

```bash
go mod download
go test ./...
```
