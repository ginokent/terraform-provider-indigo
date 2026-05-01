# 設計仕様 (SPEC.ja.md)

WebARENA Indigo (KVM VPS) 向け Terraform Provider の大方針および概要設計を記述する。詳細設計と「なぜそうなっているか」はコード内の godoc / インラインコメントに記述する方針なので、本書では結論と参照先のみを示す。

## 用途

`https://api.customer.jp/webarenaIndigo/v1` を Terraform から扱うための provider。インスタンス・SSH 鍵の CRUD と、ID 解決のためのデータソースを提供する。

提供する resource / data source:

- resource: `indigo_instance` / `indigo_ssh_key`
- data source: `indigo_instance_types` / `indigo_regions` / `indigo_oses` / `indigo_instance_specs`

## 大方針

### 1. 未公開 provider として配布する

公開 registry には登録しない。`local/indigo` source として `make install` でローカル plugin ディレクトリへ配置し、ユーザは `~/.terraformrc` の `filesystem_mirror` で解決する (詳細 `README.md`)。

### 2. Indigo API の品質問題を defensive layer で吸収する

Indigo API には次のような問題が観測されており、provider/client 層で吸収する。簡略化リファクタを行う前には必ず該当関数の godoc を読むこと。

- **レスポンス shape の揺れ**: 同じ概念に対して環境ごとに object / array / 別キー名で返ってくる
  - 対処: `decodeViaMarshal` で any → 候補型へ Marshal/Unmarshal を試行 (`internal/client/client.go`)
- **キー名の表記揺れ・typo**: `instanceTypes` / `instancetype` / `typeList` / `instancetypelist` / `instancestatus` / `ipaddress` vs `ip` 等
  - 対処: raw 構造体に複数候補キーを並べる、`Instance.UnmarshalJSON` で fallback
- **API path の揺れ**: ドキュメントとデプロイで instance type 取得の path が一致しない
  - 対処: `ListInstanceTypes` で endpoint 候補を順に試行し 404 をスキップ
- **冪等性の欠如**: `start`/`stop` が「既に running/stopped」の状態で 400 を返す
  - 対処: `isIdempotentStatusUpdateError` で成功扱いに変換
- **DELETE 専用 endpoint が存在しない**: インスタンス削除は `statusupdate` の `destroy` コマンド経由
  - 対処: `client.DeleteInstance` がそれをカプセル化、`resourceInstanceDelete` は 2 分間ポーリングして消滅確認
- **不変フィールドが 0 で返る既知バグ**: `region_id` / `os_id` / `plan_id` / `ssh_key_id` が 0 で返ることがある
  - 対処: `resourceInstanceRead` で 0 値は state を維持 (perpetual drift 回避)
- **エラーレスポンスの shape 揺れ**: `{message:...}` / `{errors:[{detail:...}]}` / `{validationErrors:{...}}` 等
  - 対処: `extractAPIErrorMessage` の再帰探索

### 3. ステータス概念を Terraform 上で 2 軸に分離する

Indigo は単一の `status` フィールドに「電源状態」と「API 応答ステータス」を混在させてくる。Terraform の resource では明確に分離する。

- `instance_status` (Optional+Computed): ユーザが指示する電源状態。`running` / `stopped` のみ受理
- `status` (Computed): API 応答ステータスを小文字化した読み取り値
- `status_raw` (Computed): 生の status 文字列 (debug 用)
- `normalizePowerStatus` で Indigo が返す多様な文字列 (`active`/`ready`/`forcestop`/`close`/`open` 等) を 2 値に正規化

### 4. 後方互換性は考慮しない

セマンティクスのクリーンさを優先する。fallback / 互換 shim を入れない。

## アーキテクチャ概要

```
main.go                       # plugin.Serve のみ
internal/provider/            # Terraform schema / CRUD
  provider.go                   provider schema, ConfigureContextFunc
  resource_instance.go          indigo_instance
  resource_ssh_key.go           indigo_ssh_key
  data_source_*.go              data sources
  diag_helpers.go               opDiag (エラーの diag.Diagnostics ラップ)
internal/client/              # HTTP client
  client.go                     OAuth2 client_credentials → Bearer
                                レート制限 (600ms 間隔)、429/5xx で最大 5 回リトライ、Retry-After 尊重
                                Indigo API の防御的デコード
```

設計判断の根拠と各関数の rationale はコード内の godoc を読むこと。本書では SPEC として「これが決定である」ことだけを示し、理由はコードに置く。

## 参照

- 使い方 / インストール / 環境構築: `README.md`
- 詳細設計と rationale: 各 `.go` ファイルの godoc / インラインコメント
- Indigo API ドキュメント: <https://indigo.arena.ne.jp/userapi/>
