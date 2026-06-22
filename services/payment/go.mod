module github.com/aizorix/platform/payment

go 1.25.0

require (
	github.com/aizorix/platform/pkg v0.0.0
	github.com/go-chi/chi/v5 v5.0.12
	github.com/jackc/pgx/v5 v5.9.2
	github.com/stripe/stripe-go/v79 v79.12.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

replace github.com/aizorix/platform/pkg => ../pkg
