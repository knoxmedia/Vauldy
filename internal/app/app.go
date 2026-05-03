package app

import (
	"database/sql"

	"knox-media/internal/config"
)

type App struct {
	Config *config.Config
	DB     *sql.DB
}
