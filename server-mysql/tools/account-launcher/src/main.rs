#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[cfg(not(target_os = "windows"))]
compile_error!("mhq-account-launcher only supports Windows");

mod app;
mod config;
mod crypto;
mod game;
mod team_plan;

use eframe::egui;

fn main() -> eframe::Result {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_inner_size([1080.0, 620.0])
            .with_min_inner_size([900.0, 480.0]),
        ..Default::default()
    };

    eframe::run_native(
        "梦幻奇遇记多账号启动器",
        options,
        Box::new(|creation_context| Ok(Box::new(app::LauncherApp::new(creation_context)))),
    )
}
