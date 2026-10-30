$ErrorActionPreference = "Stop"
$SqlcVersion = "v1.29.0"
go run "github.com/sqlc-dev/sqlc/cmd/sqlc@$SqlcVersion" generate -f internal/persistence/sqlc.yaml

