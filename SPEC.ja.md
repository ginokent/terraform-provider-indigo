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
- **キー名の表記揺れ**: `instanceTypes` / `instancetype` / `typeList` / `instancetypelist` / `ipaddress` vs `ip` 等、同概念で複数の名前が混在
  - 対処: raw 構造体に複数候補キーを並べ、非 nil の最初を採用 (`Instance.UnmarshalJSON` 等)
- **API path の揺れ**: ドキュメントとデプロイで instance type 取得の path が一致しない
  - 対処: `ListInstanceTypes` で endpoint 候補を順に試行し 404 をスキップ
- **冪等性の欠如**: `start`/`stop` が「既に running/stopped」の状態で 400 を返す
  - 対処: `isIdempotentStatusUpdateError` で成功扱いに変換
- **DELETE 専用 endpoint が存在しない**: インスタンス削除は `statusupdate` の `destroy` コマンド経由
  - 対処: `client.DeleteInstance` がそれをカプセル化、`resourceInstanceDelete` は 2 分間ポーリングして消滅確認
- **一部 ID が 0 で返る場合がある**: `os_id` / `plan_id` / `sshkey_id` が稀に 0 で返るレース条件
  - 対処: `resourceInstanceRead` で 0 値は state を維持 (perpetual drift 回避)
- **エラーレスポンスの shape 揺れ**: `{message:...}` / `{errors:[{detail:...}]}` / `{validationErrors:{...}}` 等
  - 対処: `extractAPIErrorMessage` の再帰探索

### 3. ステータス概念を 2 軸 (lifecycle / power) として明確に分離する

Indigo API は instance に対して **2 つの別概念のフィールド** を返す。これを混同してはいけない (旧実装は混同しており、provisioning 完了状態 `OPEN` を power の "stopped" にマップする等の不整合があった)。

| 概念 | API field | 観測値 | Go (Instance struct) | Terraform schema |
|---|---|---|---|---|
| lifecycle (リソース管理面) | `status` | `READY` (provisioning 中) → `OPEN` (provisioning 完了) | `LifecycleStatus` | `status` (Computed) |
| power (VM の電源状態) | `instancestatus` | `Running` / `Stopped` / `OS installation In Progress` 等 | `PowerStatus` | `instance_status` (Optional+Computed)、`status_raw` (Computed: 生値) |

- `Instance.UnmarshalJSON` は両者を **独立に** 設定する。片方が空でももう一方の値を流用しない
- `normalizePowerStatus` は power 専用。観測実値 `Running` / `Stopped` を enum 値 `RUNNING` / `STOPPED` に正規化し、それ以外 (遷移中文字列など) は uppercased+trimmed で素通し
- `instance_status` はユーザ入力としては `RUNNING` / `STOPPED` のみ受理 (ValidateFunc)。case-insensitive で受けて UPPER_CASE で state に保存 (StateFunc)
- `status` (lifecycle) も同様に UPPER_CASE で state に保存 (`READY` / `OPEN`)

### 4. インスタンスの provisioning 完了と power 収束を Create / Update で待ち合わせる

Indigo は create 後に **provisioning → 自動 boot → 自動停止** という遷移を自動で行い、ユーザが触れる状態 (`status=OPEN` / `instancestatus=Stopped`) になる。

- `resourceInstanceCreate`: createinstance → `LifecycleStatus=OPEN` までポーリング (15 分) → desired が `RUNNING` なら `start` を発行し power が `RUNNING` まで収束するのを待つ (5 分)
- `resourceInstanceUpdate`: start/stop 発行後に `PowerStatus` が desired に収束するまで待つ (5 分)
- `resourceInstanceRead`: 観測値を **常に** state へ書き戻す。desired を理由に上書きを抑止しない (drift を terraform に検出させるため)

### 5. region_id は API レスポンスに含まれない前提で扱う

`getinstancelist` 等のレスポンスは `regionname` (文字列) のみを返し、`region_id` (数値) は返さない。Terraform 側では:

- `Instance` 構造体に `RegionID` フィールドを **持たない**
- `region_id` schema は `Required` + `ForceNew`。create 時にユーザが与えた値が state に保持される
- `resourceInstanceRead` は `region_id` を API から復元しない (そもそもデータがない)

### 6. 後方互換性は考慮しない

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
