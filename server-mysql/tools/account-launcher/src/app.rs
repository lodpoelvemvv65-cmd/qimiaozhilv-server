use std::{path::PathBuf, sync::Arc, time::Duration};

use eframe::egui::{self, Color32, RichText};

use crate::{
    config::{self, AccountConfig, LauncherConfig},
    game, team_plan,
};

pub struct LauncherApp {
    config_path: Option<PathBuf>,
    game_dir: String,
    rows: Vec<AccountRow>,
    leaders_only: bool,
    next_id: u64,
    notice: Notice,
}

struct AccountRow {
    id: u64,
    launch_selected: bool,
    team_selected: bool,
    team_leader: bool,
    username: String,
    password: String,
    show_password: bool,
    process: Option<game::ManagedProcess>,
    status: String,
}

#[derive(Default)]
struct Notice {
    text: String,
    is_error: bool,
}

enum RowAction {
    Start(u64),
    StartSelected,
    StartAll,
    StopAll,
    AutoTeam,
    ShowLeader(u64),
    Stop(u64),
    Delete(u64),
}

impl LauncherApp {
    pub fn new(creation_context: &eframe::CreationContext<'_>) -> Self {
        install_chinese_font(&creation_context.egui_ctx);
        apply_style(&creation_context.egui_ctx);

        let mut notice = Notice::default();
        let config_path = match config::config_path() {
            Ok(path) => Some(path),
            Err(error) => {
                notice = Notice {
                    text: error,
                    is_error: true,
                };
                None
            }
        };
        let loaded = config_path.as_deref().map(config::load).transpose();
        let loaded = match loaded {
            Ok(Some(config)) => config,
            Ok(None) => LauncherConfig::default(),
            Err(error) => {
                notice = Notice {
                    text: error,
                    is_error: true,
                };
                LauncherConfig::default()
            }
        };
        let next_id = loaded
            .accounts
            .iter()
            .map(|account| account.id)
            .max()
            .unwrap_or(0)
            .saturating_add(1);
        let executable = game::game_executable(PathBuf::from(&loaded.game_dir).as_path());
        let mut recovered_count = 0usize;
        let mut discarded_stale_process = false;
        let rows = loaded
            .accounts
            .into_iter()
            .map(|account| {
                let identity = account
                    .managed_pid
                    .zip(account.managed_started_at)
                    .map(|(pid, started_at)| game::ProcessIdentity { pid, started_at });
                let process = identity.and_then(|identity| {
                    match game::ManagedProcess::recover(identity, &executable) {
                        Ok(process) => {
                            recovered_count += 1;
                            Some(process)
                        }
                        Err(_) => {
                            discarded_stale_process = true;
                            None
                        }
                    }
                });
                let status = process
                    .as_ref()
                    .map(|process| format!("运行中 · 已恢复管理 · PID {}", process.identity().pid))
                    .unwrap_or_else(|| "未启动".to_owned());
                AccountRow {
                    id: account.id,
                    launch_selected: account.launch_selected,
                    team_selected: account.team_selected,
                    team_leader: account.team_leader,
                    username: account.username,
                    password: account.password,
                    show_password: false,
                    process,
                    status,
                }
            })
            .collect();

        if recovered_count > 0 && notice.text.is_empty() {
            notice.text = format!("已恢复管理 {recovered_count} 个游戏进程");
        }
        let mut app = Self {
            config_path,
            game_dir: loaded.game_dir,
            rows,
            leaders_only: false,
            next_id,
            notice,
        };
        if discarded_stale_process {
            app.save_now();
        }
        app
    }

    fn add_account(&mut self) {
        let id = self.next_id;
        self.next_id = self.next_id.saturating_add(1);
        self.rows.push(AccountRow {
            id,
            launch_selected: false,
            team_selected: false,
            team_leader: false,
            username: String::new(),
            password: String::new(),
            show_password: false,
            process: None,
            status: "未启动".to_owned(),
        });
        self.save_now();
    }

    fn start_account(&mut self, id: u64) {
        self.start_accounts([id], "账号");
    }

    fn start_accounts(&mut self, ids: impl IntoIterator<Item = u64>, action_name: &str) {
        let ids: Vec<u64> = ids.into_iter().collect();
        if ids.is_empty() {
            self.set_error(format!("没有可{action_name}启动的账号"));
            return;
        }

        let mut started = 0usize;
        let mut skipped = 0usize;
        let mut errors = Vec::new();
        for id in ids {
            let Some(row) = self.rows.iter().find(|row| row.id == id) else {
                continue;
            };
            if row.process.is_some() {
                skipped += 1;
                continue;
            }
            match self.launch_account(id) {
                Ok(_) => started += 1,
                Err(error) => errors.push(error),
            }
        }

        if errors.is_empty() {
            self.set_notice(format!(
                "{action_name}启动完成：新启动 {started} 个，跳过运行中 {skipped} 个"
            ));
        } else {
            self.set_error(format!(
                "{action_name}启动：成功 {started} 个，跳过 {skipped} 个，失败 {} 个；{}",
                errors.len(),
                errors.join("；")
            ));
        }
        self.save_now();
    }

    fn launch_account(&mut self, id: u64) -> Result<u32, String> {
        let Some(index) = self.rows.iter().position(|row| row.id == id) else {
            return Err("账号不存在".to_owned());
        };
        let username = self.rows[index].username.trim().to_owned();
        let password = self.rows[index].password.clone();
        if username.is_empty() || password.is_empty() {
            return Err("账号和密码不能为空".to_owned());
        }
        if has_running_duplicate(
            id,
            &username,
            self.rows
                .iter()
                .map(|row| (row.id, row.username.as_str(), row.process.is_some())),
        ) {
            return Err(format!("账号 {username} 已有一个由启动器管理的游戏进程"));
        }

        let game_dir = PathBuf::from(self.game_dir.trim());
        let process = game::launch(&game_dir, &username, &password)
            .map_err(|error| format!("账号 {username}：{error}"))?;
        let process_id = process.identity().pid;

        let row = &mut self.rows[index];
        row.process = Some(process);
        row.status = format!("运行中 · 自动登录并进游戏 · PID {process_id}");
        Ok(process_id)
    }

    fn auto_team(&mut self) {
        let selected = self
            .rows
            .iter()
            .filter(|row| row.team_selected)
            .collect::<Vec<_>>();
        if !(2..=5).contains(&selected.len()) {
            self.set_error("自动组队必须选择 2 到 5 个账号");
            return;
        }
        let leaders = selected
            .iter()
            .copied()
            .filter(|row| row.team_leader)
            .collect::<Vec<_>>();
        if leaders.len() != 1 {
            self.set_error("本次入队账号中必须恰好标记一个队长");
            return;
        }
        let leader_account = leaders[0].username.trim().to_owned();

        let mut seen_accounts = Vec::with_capacity(selected.len());
        let mut accounts = Vec::with_capacity(selected.len());
        let mut selected_ids = Vec::with_capacity(selected.len());
        for row in selected {
            let account = row.username.trim();
            if account.is_empty() || row.password.is_empty() {
                self.set_error("入队账号和密码不能为空");
                return;
            }
            if seen_accounts.iter().any(|existing| existing == account) {
                self.set_error(format!("入队账号 {account} 重复"));
                return;
            }
            seen_accounts.push(account.to_owned());
            accounts.push(team_plan::TeamAccount {
                account: account.to_owned(),
                password: row.password.clone(),
            });
            selected_ids.push(row.id);
        }

        let game_dir = PathBuf::from(self.game_dir.trim());
        if let Err(error) = game::validate_game_dir(&game_dir) {
            self.set_error(error);
            return;
        }
        let plan_message = match team_plan::submit(&game_dir, leader_account, accounts) {
            Ok(message) => message,
            Err(error) => {
                self.set_error(error);
                return;
            }
        };

        self.start_accounts(selected_ids, "组队账号");
        self.notice.text = format!("{plan_message}；{}", self.notice.text);
    }

    fn show_leader(&mut self, leader_id: u64) {
        let Some(row) = self.rows.iter().find(|row| row.id == leader_id) else {
            self.set_error("队长账号不存在");
            return;
        };
        if !row.team_leader {
            self.set_error("该账号未标记为队长");
            return;
        };
        let username = row.username.trim().to_owned();
        let Some(process) = row.process.as_ref() else {
            self.set_error(format!("队长账号 {username} 尚未启动"));
            return;
        };
        let pid = process.identity().pid;
        match process.show_window() {
            Ok(()) => self.set_notice(format!("已显示队长账号 {username} · PID {pid}")),
            Err(error) => self.set_error(format!("显示队长账号 {username} 失败：{error}")),
        }
    }

    fn stop_account(&mut self, id: u64) {
        let Some(row) = self.rows.iter_mut().find(|row| row.id == id) else {
            return;
        };
        let Some(process) = row.process.as_mut() else {
            self.set_error("该账号没有由启动器管理的游戏进程");
            return;
        };
        let process_id = process.identity().pid;
        match process.terminate() {
            Ok(_) => {
                row.process = None;
                row.status = "已结束".to_owned();
                self.save_now();
                self.set_notice(format!("已结束 PID {process_id}"));
            }
            Err(error) => {
                row.status = format!("结束失败 · PID {process_id}");
                self.set_error(format!("结束 PID {process_id} 失败：{error}"));
            }
        }
    }

    fn stop_all_accounts(&mut self) {
        let managed_count = self.rows.iter().filter(|row| row.process.is_some()).count();
        if managed_count == 0 {
            self.set_notice("当前没有由启动器管理的游戏进程");
            return;
        }

        let mut stopped = 0usize;
        let mut errors = Vec::new();
        for row in &mut self.rows {
            let Some(process) = row.process.as_mut() else {
                continue;
            };
            let process_id = process.identity().pid;
            match process.terminate() {
                Ok(()) => {
                    row.process = None;
                    row.status = "已结束".to_owned();
                    stopped += 1;
                }
                Err(error) => {
                    row.status = format!("结束失败 · PID {process_id}");
                    let account = row.username.trim();
                    let account = if account.is_empty() {
                        "未命名账号"
                    } else {
                        account
                    };
                    errors.push(format!("{account}（PID {process_id}）：{error}"));
                }
            }
        }
        self.save_now();

        if errors.is_empty() {
            self.set_notice(format!("已结束全部 {stopped} 个游戏进程"));
        } else {
            self.set_error(format!(
                "结束全部：成功 {stopped} 个，失败 {} 个；{}",
                errors.len(),
                errors.join("；")
            ));
        }
    }

    fn poll_processes(&mut self) {
        let mut changed = false;
        for row in &mut self.rows {
            let Some(process) = row.process.as_mut() else {
                continue;
            };
            match process.poll() {
                Ok(game::ProcessState::Exited(exit_code)) => {
                    row.process = None;
                    changed = true;
                    row.status = match exit_code {
                        Some(code) => format!("已退出 · 代码 {code}"),
                        None => "已退出".to_owned(),
                    };
                }
                Ok(game::ProcessState::Replaced) => {
                    row.process = None;
                    changed = true;
                    row.status = "管理记录已失效 · PID 已被重用或程序不匹配".to_owned();
                }
                Ok(game::ProcessState::Running) => {}
                Err(error) => row.status = format!("进程状态读取失败：{error}"),
            }
        }
        if changed {
            self.save_now();
        }
    }

    fn save_now(&mut self) {
        let Some(path) = self.config_path.as_deref() else {
            return;
        };
        let config = LauncherConfig {
            game_dir: self.game_dir.clone(),
            accounts: self
                .rows
                .iter()
                .map(|row| AccountConfig {
                    id: row.id,
                    username: row.username.clone(),
                    password: row.password.clone(),
                    launch_selected: row.launch_selected,
                    team_selected: row.team_selected,
                    team_leader: row.team_leader,
                    managed_pid: row.process.as_ref().map(|process| process.identity().pid),
                    managed_started_at: row
                        .process
                        .as_ref()
                        .map(|process| process.identity().started_at),
                })
                .collect(),
        };
        if let Err(error) = config::save(path, &config) {
            self.set_error(error);
        }
    }

    fn set_notice(&mut self, message: impl Into<String>) {
        self.notice = Notice {
            text: message.into(),
            is_error: false,
        };
    }

    fn set_error(&mut self, message: impl Into<String>) {
        self.notice = Notice {
            text: message.into(),
            is_error: true,
        };
    }
}

impl eframe::App for LauncherApp {
    fn update(&mut self, context: &egui::Context, _frame: &mut eframe::Frame) {
        self.poll_processes();
        if self.rows.iter().any(|row| row.process.is_some()) {
            context.request_repaint_after(Duration::from_millis(500));
        }

        egui::TopBottomPanel::top("game_directory")
            .exact_height(108.0)
            .show(context, |ui| {
                ui.add_space(12.0);
                ui.heading("梦幻奇遇记多账号启动器");
                ui.add_space(8.0);
                ui.horizontal(|ui| {
                    ui.label("游戏目录");
                    let changed = ui
                        .add_sized(
                            [ui.available_width() - 104.0, 30.0],
                            egui::TextEdit::singleline(&mut self.game_dir),
                        )
                        .changed();
                    if ui.button("选择目录").clicked()
                        && let Some(directory) = rfd::FileDialog::new()
                            .set_title("选择梦幻奇遇记游戏目录")
                            .pick_folder()
                    {
                        self.game_dir = directory.display().to_string();
                        self.save_now();
                    }
                    if changed {
                        self.save_now();
                    }
                });
            });

        egui::TopBottomPanel::bottom("status_bar")
            .exact_height(38.0)
            .show(context, |ui| {
                ui.add_space(8.0);
                if self.notice.text.is_empty() {
                    ui.label(
                        RichText::new("账号密码使用 Windows DPAPI 加密保存").color(Color32::GRAY),
                    );
                } else {
                    let color = if self.notice.is_error {
                        Color32::from_rgb(205, 70, 70)
                    } else {
                        Color32::from_rgb(55, 145, 100)
                    };
                    ui.label(RichText::new(&self.notice.text).color(color));
                }
            });

        egui::CentralPanel::default().show(context, |ui| {
            let mut action = None;
            let mut edited = false;
            ui.horizontal(|ui| {
                ui.heading("账号");
                ui.add_space(10.0);
                ui.checkbox(&mut self.leaders_only, "只看队长");
                ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                    if ui.button("添加账号").clicked() {
                        self.add_account();
                    }
                    if ui.button("自动组队").clicked() {
                        action = Some(RowAction::AutoTeam);
                    }
                    if ui
                        .add(egui::Button::new(
                            RichText::new("结束全部").color(Color32::from_rgb(205, 70, 70)),
                        ))
                        .on_hover_text("结束启动器管理的全部游戏进程")
                        .clicked()
                    {
                        action = Some(RowAction::StopAll);
                    }
                    if ui.button("启动全部").clicked() {
                        action = Some(RowAction::StartAll);
                    }
                    if ui.button("启动所选").clicked() {
                        action = Some(RowAction::StartSelected);
                    }
                    if ui.button("清空选择").clicked() {
                        for row in &mut self.rows {
                            row.launch_selected = false;
                        }
                        edited = true;
                    }
                    if ui.button("全选").clicked() {
                        for row in &mut self.rows {
                            row.launch_selected = true;
                        }
                        edited = true;
                    }
                });
            });
            ui.separator();

            egui::ScrollArea::vertical().show(ui, |ui| {
                egui::Grid::new("account_grid")
                    .num_columns(11)
                    .spacing([10.0, 10.0])
                    .striped(true)
                    .show(ui, |ui| {
                        ui.strong("启动");
                        ui.strong("入队");
                        ui.strong("队长");
                        ui.strong("账号");
                        ui.strong("密码");
                        ui.strong("显示");
                        ui.strong("状态");
                        ui.strong("队长窗口");
                        ui.label("");
                        ui.label("");
                        ui.label("");
                        ui.end_row();

                        let leaders_only = self.leaders_only;
                        for row in &mut self.rows {
                            if leaders_only && !row.team_leader {
                                continue;
                            }
                            edited |= ui.checkbox(&mut row.launch_selected, "").changed();
                            edited |= ui.checkbox(&mut row.team_selected, "").changed();
                            let leader_changed = ui.checkbox(&mut row.team_leader, "").changed();
                            if leader_changed && row.team_leader {
                                row.team_selected = true;
                            }
                            edited |= leader_changed;
                            edited |= ui
                                .add_sized(
                                    [135.0, 28.0],
                                    egui::TextEdit::singleline(&mut row.username),
                                )
                                .changed();
                            let mut password_edit = egui::TextEdit::singleline(&mut row.password);
                            if !row.show_password {
                                password_edit = password_edit.password(true);
                            }
                            edited |= ui.add_sized([135.0, 28.0], password_edit).changed();
                            ui.checkbox(&mut row.show_password, "");
                            ui.add_sized([190.0, 28.0], egui::Label::new(&row.status).truncate());

                            if ui
                                .add_enabled(
                                    row.team_leader && row.process.is_some(),
                                    egui::Button::new("显示"),
                                )
                                .on_hover_text("恢复并切换到这个队长的游戏窗口")
                                .clicked()
                            {
                                action = Some(RowAction::ShowLeader(row.id));
                            }

                            let can_start = row.process.is_none();
                            if ui
                                .add_enabled(can_start, egui::Button::new("启动游戏"))
                                .clicked()
                            {
                                action = Some(RowAction::Start(row.id));
                            }
                            if ui
                                .add_enabled(row.process.is_some(), egui::Button::new("结束游戏"))
                                .clicked()
                            {
                                action = Some(RowAction::Stop(row.id));
                            }
                            if ui
                                .add_enabled(row.process.is_none(), egui::Button::new("删除"))
                                .clicked()
                            {
                                action = Some(RowAction::Delete(row.id));
                            }
                            ui.end_row();
                        }
                    });
            });

            if edited {
                self.save_now();
            }
            match action {
                Some(RowAction::Start(id)) => self.start_account(id),
                Some(RowAction::StartSelected) => {
                    let ids = self
                        .rows
                        .iter()
                        .filter(|row| row.launch_selected)
                        .map(|row| row.id)
                        .collect::<Vec<_>>();
                    self.start_accounts(ids, "所选账号");
                }
                Some(RowAction::StartAll) => {
                    let ids = self.rows.iter().map(|row| row.id).collect::<Vec<_>>();
                    self.start_accounts(ids, "全部账号");
                }
                Some(RowAction::StopAll) => self.stop_all_accounts(),
                Some(RowAction::AutoTeam) => self.auto_team(),
                Some(RowAction::ShowLeader(id)) => self.show_leader(id),
                Some(RowAction::Stop(id)) => self.stop_account(id),
                Some(RowAction::Delete(id)) => {
                    self.rows
                        .retain(|row| row.id != id || row.process.is_some());
                    self.save_now();
                }
                None => {}
            }
        });
    }
}

fn has_running_duplicate<'a>(
    target_id: u64,
    target_username: &str,
    mut rows: impl Iterator<Item = (u64, &'a str, bool)>,
) -> bool {
    rows.any(|(id, username, running)| {
        id != target_id && running && username.trim() == target_username
    })
}

fn install_chinese_font(context: &egui::Context) {
    let Ok(font_bytes) = std::fs::read(r"C:\Windows\Fonts\msyh.ttc") else {
        return;
    };
    let mut fonts = egui::FontDefinitions::default();
    fonts.font_data.insert(
        "microsoft-yahei".to_owned(),
        Arc::new(egui::FontData::from_owned(font_bytes)),
    );
    for family in [egui::FontFamily::Proportional, egui::FontFamily::Monospace] {
        fonts
            .families
            .entry(family)
            .or_default()
            .insert(0, "microsoft-yahei".to_owned());
    }
    context.set_fonts(fonts);
}

fn apply_style(context: &egui::Context) {
    let mut style = (*context.style()).clone();
    style.spacing.item_spacing = egui::vec2(8.0, 8.0);
    style.visuals.widgets.noninteractive.bg_fill = Color32::from_rgb(245, 246, 248);
    style.visuals.panel_fill = Color32::from_rgb(250, 250, 251);
    style.visuals.extreme_bg_color = Color32::WHITE;
    context.set_style(style);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn blocks_a_second_running_instance_of_the_same_account() {
        let rows = [(1, "account01", true), (2, "account02", false)];
        assert!(has_running_duplicate(2, "account01", rows.into_iter()));
        assert!(!has_running_duplicate(2, "account02", rows.into_iter()));
    }
}
