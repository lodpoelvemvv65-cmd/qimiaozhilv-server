package skillcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Skill struct {
	ID      int32  `json:"id"`
	Name    string `json:"name"`
	JobType int32  `json:"jobType"`
	JobName string `json:"jobName"`
}

func Load(ctx context.Context, db *sql.DB) ([]Skill, error) {
	if db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	rows, err := db.QueryContext(ctx, `SELECT root.array_index,
		COALESCE(MAX(CASE WHEN field.field_name = 'Name' THEN field.string_value END), '')
		FROM game_config_nodes root
		LEFT JOIN game_config_nodes field ON field.parent_id = root.node_id
			AND field.config_name = root.config_name AND field.node_kind = 3
			AND field.field_name = 'Name'
		WHERE root.parent_id = 0 AND root.node_kind = 1 AND root.array_index > 0
			AND root.config_name = 'SkillConfig' AND MOD(root.array_index, 100) = 0
		GROUP BY root.node_id, root.array_index
		ORDER BY root.array_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	skills := make([]Skill, 0)
	seen := make(map[int32]struct{})
	for rows.Next() {
		var configID int64
		var name string
		if err := rows.Scan(&configID, &name); err != nil {
			return nil, err
		}
		id := int32(configID / 100)
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		skills = append(skills, classify(id, name))
	}
	return skills, rows.Err()
}

func NamesByID(skills []Skill) map[int32]string {
	names := make(map[int32]string, len(skills))
	for _, skill := range skills {
		names[skill.ID] = skill.Name
	}
	return names
}

func DisplayName(id int32, names map[int32]string) string {
	if names != nil {
		if name := strings.TrimSpace(names[id]); name != "" {
			return name
		}
	}
	return "未命名技能"
}

func classify(id int32, name string) Skill {
	skill := Skill{ID: id, Name: strings.TrimSpace(name), JobType: id / 100000}
	if skill.Name == "" {
		skill.Name = "未命名技能"
	}
	switch skill.JobType {
	case 1:
		skill.JobName = "军官"
	case 2:
		skill.JobName = "运动员"
	case 3:
		skill.JobName = "护士"
	case 4:
		skill.JobName = "超人"
	default:
		skill.JobName = "通用"
		skill.JobType = 0
	}
	return skill
}
