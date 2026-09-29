use std::{
    path::{Path, PathBuf},
    process::{Child, Command},
};

use windows::{
    Win32::{
        Foundation::{CloseHandle, FILETIME, HANDLE, HWND, LPARAM, WAIT_OBJECT_0, WAIT_TIMEOUT},
        System::Threading::{
            GetProcessTimes, OpenProcess, PROCESS_ACCESS_RIGHTS, PROCESS_QUERY_LIMITED_INFORMATION,
            PROCESS_SYNCHRONIZE, PROCESS_TERMINATE, QueryFullProcessImageNameW, TerminateProcess,
            WaitForSingleObject,
        },
        UI::WindowsAndMessaging::{
            BringWindowToTop, EnumWindows, GetWindowTextLengthW, GetWindowThreadProcessId,
            IsWindowVisible, SW_RESTORE, SetForegroundWindow, ShowWindowAsync,
        },
    },
    core::{BOOL, PWSTR},
};

pub const GAME_EXE_NAME: &str = "梦幻奇遇记.exe";
pub const ACCOUNT_ENV: &str = "MHQ_LAUNCHER_ACCOUNT";
pub const PASSWORD_ENV: &str = "MHQ_LAUNCHER_PASSWORD";
pub const AUTO_ENTER_ENV: &str = "MHQ_LAUNCHER_AUTO_ENTER";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ProcessIdentity {
    pub pid: u32,
    pub started_at: u64,
}

pub struct ManagedProcess {
    child: Option<Child>,
    identity: ProcessIdentity,
    executable: PathBuf,
}

pub enum ProcessState {
    Running,
    Exited(Option<i32>),
    Replaced,
}

struct OwnedHandle(HANDLE);

struct WindowSearch {
    pid: u32,
    window: Option<HWND>,
}

impl Drop for OwnedHandle {
    fn drop(&mut self) {
        // SAFETY: this wrapper is created only for successful OpenProcess handles.
        let _ = unsafe { CloseHandle(self.0) };
    }
}

pub fn game_executable(game_dir: &Path) -> PathBuf {
    game_dir.join(GAME_EXE_NAME)
}

pub fn validate_game_dir(game_dir: &Path) -> Result<PathBuf, String> {
    if !game_dir.is_dir() {
        return Err("请选择有效的游戏目录".to_owned());
    }
    let executable = game_executable(game_dir);
    if !executable.is_file() {
        return Err(format!("所选目录中没有 {GAME_EXE_NAME}"));
    }
    Ok(executable)
}

pub fn launch(game_dir: &Path, username: &str, password: &str) -> Result<ManagedProcess, String> {
    let executable = validate_game_dir(game_dir)?;
    let child = Command::new(&executable)
        .current_dir(game_dir)
        .env(ACCOUNT_ENV, username)
        .env(PASSWORD_ENV, password)
        .env(AUTO_ENTER_ENV, "1")
        .spawn()
        .map_err(|error| format!("启动 {} 失败：{error}", executable.display()))?;
    ManagedProcess::from_child(child, &executable)
}

impl ManagedProcess {
    fn from_child(child: Child, executable: &Path) -> Result<Self, String> {
        let pid = child.id();
        let (_, started_at) = inspect_process(pid)?;
        let process = Self {
            child: Some(child),
            identity: ProcessIdentity { pid, started_at },
            executable: executable.to_owned(),
        };
        process.validate_identity()?;
        Ok(process)
    }

    pub fn recover(identity: ProcessIdentity, executable: &Path) -> Result<Self, String> {
        let process = Self {
            child: None,
            identity,
            executable: executable.to_owned(),
        };
        process.validate_identity()?;
        Ok(process)
    }

    pub fn identity(&self) -> ProcessIdentity {
        self.identity
    }

    pub fn poll(&mut self) -> Result<ProcessState, String> {
        if let Some(child) = self.child.as_mut() {
            return child
                .try_wait()
                .map(|status| match status {
                    Some(status) => ProcessState::Exited(status.code()),
                    None => ProcessState::Running,
                })
                .map_err(|error| format!("进程状态读取失败：{error}"));
        }

        let access = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE;
        match open_process(access, self.identity.pid) {
            Ok(handle) => {
                // SAFETY: the handle remains valid for this query.
                if unsafe { WaitForSingleObject(handle.0, 0) } == WAIT_OBJECT_0 {
                    return Ok(ProcessState::Exited(None));
                }
                let (path, started_at) = inspect_handle(handle.0)?;
                if started_at != self.identity.started_at
                    || !same_executable(&path, &self.executable)
                {
                    Ok(ProcessState::Replaced)
                } else {
                    Ok(ProcessState::Running)
                }
            }
            Err(error) if process_is_gone(&error) => Ok(ProcessState::Exited(None)),
            Err(error) => Err(error),
        }
    }

    pub fn terminate(&mut self) -> Result<(), String> {
        let access = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE | PROCESS_TERMINATE;
        let handle = open_process(access, self.identity.pid)?;
        self.validate_with_handle(handle.0)?;
        // SAFETY: the handle belongs to the validated process and includes PROCESS_TERMINATE.
        unsafe { TerminateProcess(handle.0, 1) }
            .map_err(|error| format!("结束 PID {} 失败：{error}", self.identity.pid))?;
        // SAFETY: the handle remains valid for this call and includes synchronization access.
        let wait = unsafe { WaitForSingleObject(handle.0, 10_000) };
        if wait != WAIT_OBJECT_0 {
            return Err(format!("等待 PID {} 结束超时", self.identity.pid));
        }
        if let Some(child) = self.child.as_mut() {
            let _ = child.wait();
        }
        Ok(())
    }

    pub fn show_window(&self) -> Result<(), String> {
        self.validate_identity()?;
        let mut search = WindowSearch {
            pid: self.identity.pid,
            window: None,
        };
        // SAFETY: EnumWindows invokes the callback synchronously while search is alive.
        unsafe {
            EnumWindows(
                Some(find_process_window),
                LPARAM((&mut search as *mut WindowSearch) as isize),
            )
        }
        .map_err(|error| format!("查找 PID {} 的游戏窗口失败：{error}", self.identity.pid))?;
        let window = search
            .window
            .ok_or_else(|| format!("PID {} 尚未创建可显示的游戏窗口", self.identity.pid))?;

        // SAFETY: the HWND was returned by EnumWindows for the validated process.
        unsafe {
            let _ = ShowWindowAsync(window, SW_RESTORE);
            BringWindowToTop(window).map_err(|error| {
                format!("显示 PID {} 的游戏窗口失败：{error}", self.identity.pid)
            })?;
            if !SetForegroundWindow(window).as_bool() {
                return Err(format!(
                    "Windows 阻止了 PID {} 的窗口切换",
                    self.identity.pid
                ));
            }
        }
        Ok(())
    }

    fn validate_identity(&self) -> Result<(), String> {
        let access = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE;
        let handle = open_process(access, self.identity.pid)?;
        self.validate_with_handle(handle.0)
    }

    fn validate_with_handle(&self, handle: HANDLE) -> Result<(), String> {
        // SAFETY: the handle remains valid for this query.
        if unsafe { WaitForSingleObject(handle, 0) } != WAIT_TIMEOUT {
            return Err(format!("PID {} 已结束", self.identity.pid));
        }
        let (path, started_at) = inspect_handle(handle)?;
        if started_at != self.identity.started_at {
            return Err(format!("PID {} 已被 Windows 重用", self.identity.pid));
        }
        if !same_executable(&path, &self.executable) {
            return Err(format!("PID {} 不是当前选择的游戏程序", self.identity.pid));
        }
        Ok(())
    }
}

unsafe extern "system" fn find_process_window(window: HWND, param: LPARAM) -> BOOL {
    // SAFETY: show_window passes a live WindowSearch pointer for this synchronous enumeration.
    let search = unsafe { &mut *(param.0 as *mut WindowSearch) };
    if search.window.is_some() || !unsafe { IsWindowVisible(window) }.as_bool() {
        return BOOL(1);
    }
    let mut pid = 0u32;
    unsafe { GetWindowThreadProcessId(window, Some(&mut pid)) };
    if pid == search.pid && unsafe { GetWindowTextLengthW(window) } > 0 {
        search.window = Some(window);
    }
    BOOL(1)
}

fn inspect_process(pid: u32) -> Result<(PathBuf, u64), String> {
    let access = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE;
    let handle = open_process(access, pid)?;
    inspect_handle(handle.0)
}

fn inspect_handle(handle: HANDLE) -> Result<(PathBuf, u64), String> {
    let mut path_buffer = vec![0u16; 32_768];
    let mut path_len = path_buffer.len() as u32;
    // SAFETY: the output buffer is writable for path_len UTF-16 code units.
    unsafe {
        QueryFullProcessImageNameW(
            handle,
            Default::default(),
            PWSTR(path_buffer.as_mut_ptr()),
            &mut path_len,
        )
    }
    .map_err(|error| format!("读取游戏进程路径失败：{error}"))?;
    path_buffer.truncate(path_len as usize);

    let mut creation = FILETIME::default();
    let mut exit = FILETIME::default();
    let mut kernel = FILETIME::default();
    let mut user = FILETIME::default();
    // SAFETY: all FILETIME pointers are valid writable outputs for this process handle.
    unsafe { GetProcessTimes(handle, &mut creation, &mut exit, &mut kernel, &mut user) }
        .map_err(|error| format!("读取游戏进程创建时间失败：{error}"))?;
    let started_at = ((creation.dwHighDateTime as u64) << 32) | creation.dwLowDateTime as u64;
    Ok((
        PathBuf::from(String::from_utf16_lossy(&path_buffer)),
        started_at,
    ))
}

fn open_process(access: PROCESS_ACCESS_RIGHTS, pid: u32) -> Result<OwnedHandle, String> {
    // SAFETY: OpenProcess does not borrow caller-owned memory; the returned handle is owned below.
    unsafe { OpenProcess(access, false, pid) }
        .map(OwnedHandle)
        .map_err(|error| format!("打开 PID {pid} 失败：{error}"))
}

fn same_executable(actual: &Path, expected: &Path) -> bool {
    normalized_path(actual) == normalized_path(expected)
}

fn normalized_path(path: &Path) -> String {
    let canonical = std::fs::canonicalize(path).unwrap_or_else(|_| path.to_owned());
    canonical
        .to_string_lossy()
        .trim_start_matches(r"\\?\")
        .to_lowercase()
}

fn process_is_gone(error: &str) -> bool {
    error.contains("0x80070057")
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs::File,
        process::Command,
        thread,
        time::{Duration, SystemTime, UNIX_EPOCH},
    };

    #[test]
    fn validates_game_directory() {
        let suffix = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("time")
            .as_nanos();
        let directory = std::env::temp_dir().join(format!("mhq-game-dir-{suffix}"));
        std::fs::create_dir_all(&directory).expect("create directory");
        assert!(validate_game_dir(&directory).is_err());
        File::create(directory.join(GAME_EXE_NAME)).expect("create executable");
        assert!(validate_game_dir(&directory).is_ok());
        std::fs::remove_dir_all(directory).expect("cleanup");
    }

    #[test]
    fn recovers_and_terminates_a_managed_process() {
        const PROBE_ENV: &str = "MHQ_LAUNCHER_PROCESS_PROBE";
        if std::env::var_os(PROBE_ENV).is_some() {
            thread::sleep(Duration::from_secs(30));
            return;
        }

        let executable = std::env::current_exe().expect("current test executable");
        let child = Command::new(&executable)
            .args([
                "--exact",
                "game::tests::recovers_and_terminates_a_managed_process",
            ])
            .env(PROBE_ENV, "1")
            .spawn()
            .expect("spawn process probe");
        let managed = ManagedProcess::from_child(child, &executable).expect("manage child");
        let identity = managed.identity();
        drop(managed);

        let mut recovered =
            ManagedProcess::recover(identity, &executable).expect("recover process");
        assert!(matches!(
            recovered.poll().expect("poll"),
            ProcessState::Running
        ));
        recovered.terminate().expect("terminate recovered process");
        assert!(matches!(
            recovered.poll().expect("poll terminated process"),
            ProcessState::Exited(None)
        ));
    }
}
