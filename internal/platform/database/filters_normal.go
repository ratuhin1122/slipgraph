//go:build !rlstest

package database

func tenantSQL(query string) string { return query }
