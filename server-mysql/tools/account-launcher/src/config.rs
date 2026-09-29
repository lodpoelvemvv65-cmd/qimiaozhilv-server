use std::{
    env, fs,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};

use crate::crypto;

const CONFIG_VERSION: u32 = 1;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct LauncherConfig {
    pub game_dir: String,
    pub accounts: Vec<AccountConfig>,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct AccountConfig {
    pub id: u64,
    pub username: String,
    pub password: String,
    pub launch_selected: bool,
    pub team_selected: bool,
    pub team_leader: bool,
    pub managed_pid: Option<u32>,
    pub managed_started_at: Option<u64>,
}

#[derive(Serialize, Deserialize)]
struct StoredConfig {
    version: u32,
    game_dir: String,
    // Version 1 originally stored one global leader. Keep reading it so
    // existing installations migrate that row to the multi-leader flag.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    team_leader_id: Option<u64>,
    accounts: Vec<StoredAccount>,
}

#[derive(Serialize, Deserialize)]
struct StoredAccount {
    id: u64,
    username: String,
    password_dpapi: String,
    #[serde(default)]
    launch_selected: bool,
    #[serde(default)]
    team_selected: bool,
    #[serde(default)]
    team_leader: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    managed_pid: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    managed_started_at: Option<u64>,
}

pub fn config_path() -> Result<PathBuf, String> {
    let app_data = env::var_os("APPDATA").ok_or("找不到 Windows APPDATA 目录")?;
    Ok(PathBuf::from(app_data)
        .join("MHQAccountLauncher")
        .join("accounts.json"))
}

pub fn load(path: &Path) -> Result<LauncherConfig, String> {
    if !path.exists() {
        return Ok(LauncherConfig::default());
    }
    let contents = fs::read_to_string(path)
        .map_err(|error| format!("读取配置 {} 失败：{error}", path.display()))?;
    let stored: StoredConfig =
        serde_json::from_str(&contents).map_err(|error| format!("配置文件格式错误：{error}"))?;
    if stored.version != CONFIG_VERSION {
        return Err(format!("不支持的配置版本：{}", stored.version));
    }

    let legacy_team_leader_id = stored.team_leader_id;
    let mut accounts = Vec::with_capacity(stored.accounts.len());
    for account in stored.accounts {
        let password = crypto::unprotect_text(&account.password_dpapi)
            .map_err(|error| format!("账号 {} 的{error}", account.username))?;
        accounts.push(AccountConfig {
            id: account.id,
            username: account.username,
            password,
            launch_selected: account.launch_selected,
            team_selected: account.team_selected,
            team_leader: account.team_leader || legacy_team_leader_id == Some(account.id),
            managed_pid: account.managed_pid,
            managed_started_at: account.managed_started_at,
        });
    }
    Ok(LauncherConfig {
        game_dir: stored.game_dir,
        accounts,
    })
}

pub fn save(path: &Path, config: &LauncherConfig) -> Result<(), String> {
    let mut accounts = Vec::with_capacity(config.accounts.len());
    for account in &config.accounts {
        accounts.push(StoredAccount {
            id: account.id,
            username: account.username.clone(),
            launch_selected: account.launch_selected,
            team_selected: account.team_selected,
            team_leader: account.team_leader,
            managed_pid: account.managed_pid,
            managed_started_at: account.managed_started_at,
            password_dpapi: crypto::protect_text(&account.password)
                .map_err(|error| format!("账号 {} 的{error}", account.username))?,
        });
    }
    let stored = StoredConfig {
        version: CONFIG_VERSION,
        game_dir: config.game_dir.clone(),
        team_leader_id: None,
        accounts,
    };
    let json = serde_json::to_string_pretty(&stored)
        .map_err(|error| format!("生成配置文件失败：{error}"))?;
    let parent = path.parent().ok_or("配置文件路径无效")?;
    fs::create_dir_all(parent)
        .map_err(|error| format!("创建配置目录 {} 失败：{error}", parent.display()))?;
    fs::write(path, json).map_err(|error| format!("保存配置 {} 失败：{error}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::{SystemTime, UNIX_EPOCH};

    fn test_path() -> PathBuf {
        let suffix = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("time")
            .as_nanos();
        env::temp_dir()
            .join(format!("mhq-launcher-{suffix}"))
            .join("accounts.json")
    }

    #[test]
    fn saved_config_does_not_contain_plaintext_password() {
        let path = test_path();
        let config = LauncherConfig {
            game_dir: r"D:\games\mhq".to_owned(),
            accounts: vec![AccountConfig {
                id: 1,
                username: "account01".to_owned(),
                password: "secret123".to_owned(),
                launch_selected: true,
                team_selected: true,
                team_leader: true,
                managed_pid: Some(1234),
                managed_started_at: Some(987_654_321),
            }],
        };

        save(&path, &config).expect("save");
        let json = fs::read_to_string(&path).expect("read");
        assert!(!json.contains("secret123"));
        assert_eq!(load(&path).expect("load"), config);
        fs::remove_dir_all(path.parent().expect("parent")).expect("cleanup");
    }

    #[test]
    fn loads_legacy_global_leader_as_account_flag() {
        let path = test_path();
        let parent = path.parent().expect("parent");
        fs::create_dir_all(parent).expect("create directory");
        let protected = crypto::protect_text("secret123").expect("protect password");
        let legacy = serde_json::json!({
            "version": 1,
            "game_dir": r"D:\games\mhq",
            "team_leader_id": 2,
            "accounts": [
                {"id": 1, "username": "member", "password_dpapi": protected},
                {"id": 2, "username": "leader", "password_dpapi": protected}
            ]
        });
        fs::write(&path, serde_json::to_string_pretty(&legacy).expect("json"))
            .expect("write legacy config");

        let loaded = load(&path).expect("load legacy config");
        assert!(!loaded.accounts[0].team_leader);
        assert!(loaded.accounts[1].team_leader);
        fs::remove_dir_all(parent).expect("cleanup");
    }
}
