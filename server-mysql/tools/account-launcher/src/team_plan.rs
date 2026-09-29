use std::{
    io::{Read, Write},
    net::TcpStream,
    path::Path,
    time::Duration,
};

use prost::Message;
use zeroize::Zeroize;

const LOCAL_SERVER_ADDRESS: &str = "127.0.0.1:7756";
const PUBLIC_ADDRESS_MASK: u8 = 0xa7;
const PUBLIC_ADDRESS_ENCODED: [u8; 20] = [
    0x96, 0x96, 0x96, 0x89, 0x95, 0x95, 0x9e, 0x89, 0x96, 0x93, 0x90, 0x89, 0x95, 0x93, 0x93, 0x9d,
    0x90, 0x90, 0x92, 0x91,
];

const LAUNCHER_TEAM_REQUEST_OPCODE: u16 = 65_000;
const LAUNCHER_TEAM_RESPONSE_OPCODE: u16 = 65_001;
const REQUEST_VERSION: u32 = 1;
const NETWORK_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Clone, Message)]
pub struct TeamAccount {
    #[prost(string, tag = "1")]
    pub account: String,
    #[prost(string, tag = "2")]
    pub password: String,
}

impl Drop for TeamAccount {
    fn drop(&mut self) {
        self.password.zeroize();
    }
}

#[derive(Message)]
struct TeamPlanRequest {
    #[prost(uint32, tag = "1")]
    version: u32,
    #[prost(string, tag = "2")]
    leader_account: String,
    #[prost(message, repeated, tag = "3")]
    accounts: Vec<TeamAccount>,
}

#[derive(Message)]
struct TeamPlanResponse {
    #[prost(bool, tag = "1")]
    ok: bool,
    #[prost(string, tag = "2")]
    message: String,
    #[prost(uint64, tag = "3")]
    _expires_in_seconds: u64,
}

pub fn submit(
    game_dir: &Path,
    leader_account: String,
    accounts: Vec<TeamAccount>,
) -> Result<String, String> {
    let address = server_address(game_dir);
    let mut request = TeamPlanRequest {
        version: REQUEST_VERSION,
        leader_account,
        accounts,
    };
    let mut stream = TcpStream::connect(&address).map_err(|error| {
        if is_local_game_dir(game_dir) {
            format!("连接本地服务端失败：{error}")
        } else {
            format!("连接远程服务端失败：{error}")
        }
    })?;
    stream
        .set_read_timeout(Some(NETWORK_TIMEOUT))
        .map_err(|error| format!("设置组队响应超时失败：{error}"))?;
    stream
        .set_write_timeout(Some(NETWORK_TIMEOUT))
        .map_err(|error| format!("设置组队请求超时失败：{error}"))?;

    let mut body = Vec::with_capacity(request.encoded_len());
    request
        .encode(&mut body)
        .map_err(|error| format!("生成自动组队请求失败：{error}"))?;
    clear_request_passwords(&mut request);
    let frame_length = u16::try_from(body.len() + 2).map_err(|_| {
        body.zeroize();
        "自动组队请求过大".to_owned()
    })?;

    let mut header = [0u8; 4];
    header[..2].copy_from_slice(&frame_length.to_le_bytes());
    header[2..].copy_from_slice(&LAUNCHER_TEAM_REQUEST_OPCODE.to_le_bytes());
    let write_result = stream
        .write_all(&header)
        .and_then(|_| stream.write_all(&body));
    body.zeroize();
    write_result.map_err(|error| format!("发送自动组队请求失败：{error}"))?;

    stream
        .read_exact(&mut header)
        .map_err(|error| format!("读取自动组队响应失败：{error}"))?;
    let response_length = usize::from(u16::from_le_bytes([header[0], header[1]]));
    let response_opcode = u16::from_le_bytes([header[2], header[3]]);
    if response_length < 2 || response_opcode != LAUNCHER_TEAM_RESPONSE_OPCODE {
        return Err("服务端返回了无效的自动组队响应".to_owned());
    }
    let mut response_body = vec![0u8; response_length - 2];
    stream
        .read_exact(&mut response_body)
        .map_err(|error| format!("读取自动组队响应内容失败：{error}"))?;
    let response = TeamPlanResponse::decode(response_body.as_slice())
        .map_err(|error| format!("解析自动组队响应失败：{error}"))?;
    if response.ok {
        Ok(response.message)
    } else {
        Err(response.message)
    }
}

fn clear_request_passwords(request: &mut TeamPlanRequest) {
    for account in &mut request.accounts {
        account.password.zeroize();
    }
}

fn server_address(game_dir: &Path) -> String {
    if is_local_game_dir(game_dir) {
        return LOCAL_SERVER_ADDRESS.to_owned();
    }
    let mask = std::hint::black_box(PUBLIC_ADDRESS_MASK);
    let bytes = PUBLIC_ADDRESS_ENCODED.map(|byte| byte ^ mask);
    String::from_utf8(bytes.to_vec()).expect("encoded public address is valid ASCII")
}

fn is_local_game_dir(game_dir: &Path) -> bool {
    game_dir
        .file_name()
        .and_then(|name| name.to_str())
        .is_some_and(|name| name.eq_ignore_ascii_case("client-test"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn selects_server_from_the_game_directory_name() {
        assert_eq!(
            server_address(Path::new(r"D:\games\client-test")),
            LOCAL_SERVER_ADDRESS
        );
        let expected = format!("{}.{}.{}.{}:{}", "111", "229", "147", "244", "7756");
        assert_eq!(
            server_address(Path::new(r"D:\games\public-client")),
            expected
        );
    }

    #[test]
    fn clears_serialized_request_password_copies() {
        let mut request = TeamPlanRequest {
            version: 1,
            leader_account: "leader".to_owned(),
            accounts: vec![TeamAccount {
                account: "leader".to_owned(),
                password: "secret123".to_owned(),
            }],
        };
        clear_request_passwords(&mut request);
        assert!(
            request.accounts[0]
                .password
                .chars()
                .all(|character| character == '\0')
        );
    }
}
