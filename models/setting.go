package models

import (
	"time"

	"gorm.io/gorm"
)

type SystemSetting struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	Key         string         `gorm:"size:128;uniqueIndex;not null" json:"key"`
	Value       string         `gorm:"type:text" json:"value"`
	Type        string         `gorm:"size:32;default:string" json:"type"`
	Description string         `gorm:"size:255" json:"description"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (SystemSetting) TableName() string {
	return "system_settings"
}
