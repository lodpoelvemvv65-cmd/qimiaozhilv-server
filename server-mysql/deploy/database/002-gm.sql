-- GM 管理后台增量表。生产部署前由 server-mysql/internal/mysqlschema.Ensure
-- 或本文件执行；不修改 accounts、players 及其他玩家数据。
CREATE TABLE IF NOT EXISTS gm_admin_users (
  id BIGINT NOT NULL AUTO_INCREMENT,
  username VARCHAR(191) NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  display_name VARCHAR(191) NOT NULL DEFAULT '',
  status TINYINT NOT NULL DEFAULT 1,
  last_login_at BIGINT NULL,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_gm_admin_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS gm_roles (
  id BIGINT NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  description VARCHAR(255) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uq_gm_roles_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS gm_admin_roles (
  admin_id BIGINT NOT NULL,
  role_id BIGINT NOT NULL,
  PRIMARY KEY (admin_id, role_id),
  CONSTRAINT fk_gm_admin_roles_admin FOREIGN KEY (admin_id) REFERENCES gm_admin_users(id) ON DELETE CASCADE,
  CONSTRAINT fk_gm_admin_roles_role FOREIGN KEY (role_id) REFERENCES gm_roles(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS gm_audit_logs (
  id BIGINT NOT NULL AUTO_INCREMENT,
  request_id VARCHAR(64) NOT NULL,
  admin_id BIGINT NOT NULL DEFAULT 0,
  action VARCHAR(128) NOT NULL,
  target_type VARCHAR(64) NOT NULL DEFAULT '',
  target_id VARCHAR(191) NOT NULL DEFAULT '',
  before_json LONGTEXT NULL,
  after_json LONGTEXT NULL,
  reason VARCHAR(500) NOT NULL DEFAULT '',
  client_ip VARCHAR(64) NOT NULL DEFAULT '',
  user_agent VARCHAR(500) NOT NULL DEFAULT '',
  idempotency_key VARCHAR(191) NOT NULL DEFAULT '',
  result VARCHAR(32) NOT NULL DEFAULT '',
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_gm_audit_request (request_id),
  KEY idx_gm_audit_admin_created (admin_id, created_at),
  KEY idx_gm_audit_target (target_type, target_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS gm_command_records (
  request_id VARCHAR(64) NOT NULL,
  action VARCHAR(128) NOT NULL,
  target_id VARCHAR(191) NOT NULL DEFAULT '',
  admin_id BIGINT NOT NULL DEFAULT 0,
  status VARCHAR(32) NOT NULL,
  payload_hash CHAR(64) NOT NULL DEFAULT '',
  expected_version VARCHAR(64) NOT NULL DEFAULT '',
  game_revision BIGINT NULL,
  result_json LONGTEXT NULL,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  expires_at BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  accepted_at BIGINT NULL,
  finished_at BIGINT NULL,
  PRIMARY KEY (request_id),
  KEY idx_gm_command_status_created (status, created_at),
  KEY idx_gm_command_target (target_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS gm_config_drafts (
  draft_id BIGINT NOT NULL AUTO_INCREMENT,
  config_name VARCHAR(191) NOT NULL,
  content_yaml LONGTEXT NOT NULL,
  content_hash CHAR(64) NOT NULL,
  base_revision BIGINT NOT NULL DEFAULT 0,
  created_by BIGINT NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'draft',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (draft_id),
  KEY idx_gm_config_drafts_name_status (config_name, status, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS gm_config_snapshots (
  revision BIGINT NOT NULL,
  config_name VARCHAR(191) NOT NULL,
  content_yaml LONGTEXT NOT NULL,
  content_hash CHAR(64) NOT NULL,
  publisher VARCHAR(191) NOT NULL DEFAULT '',
  note VARCHAR(255) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  PRIMARY KEY (revision),
  KEY idx_gm_config_snapshots_name (config_name, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
