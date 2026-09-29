use std::slice;

use base64::{Engine as _, engine::general_purpose::STANDARD};
use windows::{
    Win32::{
        Foundation::{HLOCAL, LocalFree},
        Security::Cryptography::{
            CRYPT_INTEGER_BLOB, CRYPTPROTECT_UI_FORBIDDEN, CryptProtectData, CryptUnprotectData,
        },
    },
    core::PCWSTR,
};
use zeroize::Zeroize;

pub fn protect_text(plain_text: &str) -> Result<String, String> {
    let input_length = u32::try_from(plain_text.len()).map_err(|_| "密码过长".to_owned())?;
    let input = CRYPT_INTEGER_BLOB {
        cbData: input_length,
        pbData: plain_text.as_ptr().cast_mut(),
    };
    let mut output = CRYPT_INTEGER_BLOB::default();

    // DPAPI allocates output.pbData with LocalAlloc. It must be released with LocalFree.
    unsafe {
        CryptProtectData(
            &input,
            PCWSTR::null(),
            None,
            None,
            None,
            CRYPTPROTECT_UI_FORBIDDEN,
            &mut output,
        )
        .map_err(|error| format!("Windows DPAPI 加密失败：{error}"))?;
    }

    let encrypted = unsafe { slice::from_raw_parts(output.pbData, output.cbData as usize) };
    let encoded = STANDARD.encode(encrypted);
    unsafe {
        let _ = LocalFree(Some(HLOCAL(output.pbData.cast())));
    }
    Ok(encoded)
}

pub fn unprotect_text(encoded: &str) -> Result<String, String> {
    let mut encrypted = STANDARD
        .decode(encoded)
        .map_err(|error| format!("密码密文 Base64 无效：{error}"))?;
    let input_length = u32::try_from(encrypted.len()).map_err(|_| "密码密文过长".to_owned())?;
    let input = CRYPT_INTEGER_BLOB {
        cbData: input_length,
        pbData: encrypted.as_mut_ptr(),
    };
    let mut output = CRYPT_INTEGER_BLOB::default();

    let unprotect_result = unsafe {
        CryptUnprotectData(
            &input,
            None,
            None,
            None,
            None,
            CRYPTPROTECT_UI_FORBIDDEN,
            &mut output,
        )
    };
    encrypted.zeroize();
    unprotect_result.map_err(|error| format!("Windows DPAPI 解密失败：{error}"))?;

    let decrypted = unsafe { slice::from_raw_parts_mut(output.pbData, output.cbData as usize) };
    let result = String::from_utf8(decrypted.to_vec()).map_err(|error| {
        let mut bytes = error.into_bytes();
        bytes.zeroize();
        "解密后的密码不是有效文本".to_owned()
    });
    decrypted.zeroize();
    unsafe {
        let _ = LocalFree(Some(HLOCAL(output.pbData.cast())));
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dpapi_round_trip() {
        let encrypted = protect_text("a123123").expect("protect");
        assert_ne!(encrypted, "a123123");
        assert_eq!(unprotect_text(&encrypted).expect("unprotect"), "a123123");
    }
}
