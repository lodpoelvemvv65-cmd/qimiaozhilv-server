"""线上抓包 opcode 普查：某个 opcode 在线上协议里到底有没有样本。

这是离线诊断，不连接游戏服。它读 `参考数据/抓包归档/*.pcapng`，按 7756/7757/7758
三个线上游戏端口重组 TCP 流，把每个 opcode 的出现次数打出来。用途是回答
「线上到底发不发这条包」，例如 20081 M2C_BattleTouchState 在 12 份样本里是 0 条，
而客户端处理器也是空实现，因此服务端不该发它。

用法：

    python .\\test\\inspect_opcode_census.py
    python .\\test\\inspect_opcode_census.py --op 20081 20076 20396
    python .\\test\\inspect_opcode_census.py --absent 20081 20076
    python .\\test\\inspect_opcode_census.py --json logs/runtime/opcode-census.json

`--absent` 列出必须一条都没有的 opcode，命中即退出码 1，可直接当回归守卫用。
"""

import argparse
import collections
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import inspect_combat_capture as icc  # noqa: E402

SERVER = Path(__file__).resolve().parent.parent
ROOT = SERVER.parent
CAPTURE_DIR = ROOT / "参考数据" / "抓包归档"

# 线上登录网关与游戏网关实测端口：多数战斗抓包落在 7758。
GAME_PORTS = {7756, 7757, 7758}

# 本目录 inspect_combat_capture.py 的 opcode 表只覆盖战斗主链路，这里补上
# 状态、反击和杂项，便于一次普查看全。
EXTRA_NAMES = {
    20079: "M2C_SyncUnitAttribute",
    20080: "M2C_BattleChangeState",
    20081: "M2C_BattleTouchState",
    20082: "M2C_MainstoryMonsterDead",
    20085: "C2M_GetBattleStateBuff",
    20086: "M2C_GetBattleStateBuff",
    20396: "M2C_BattleStateBuff",
}

DEFAULT_INTERESTING = [20075, 20076, 20077, 20078, 20080, 20081, 20396]


def opcode_name(opcode):
    return EXTRA_NAMES.get(opcode) or icc.OPCODE_NAMES.get(opcode) or str(opcode)


def census_capture(capture, ports):
    census = collections.Counter()
    frames_seen = 0
    for (source, target), data, byte_times in icc.reassembled_streams(capture):
        if source[1] not in ports and target[1] not in ports:
            continue
        for _timestamp, opcode, _body in icc.frames(data, byte_times):
            frames_seen += 1
            census[opcode] += 1
    return frames_seen, census


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    parser = argparse.ArgumentParser(description="线上抓包 opcode 普查")
    parser.add_argument("--op", type=int, nargs="*", default=DEFAULT_INTERESTING,
                        help="要重点列出的 opcode（默认战斗/状态主链路）")
    parser.add_argument("--absent", type=int, nargs="*", default=[],
                        help="必须一条都没有的 opcode，命中即退出码 1")
    parser.add_argument("--port", type=int, nargs="*", default=sorted(GAME_PORTS),
                        help="游戏服端口（默认 7756 7757 7758）")
    parser.add_argument("--capture", type=Path, default=None, help="只跑单个 pcapng")
    parser.add_argument("--json", type=Path, default=None, help="把普查结果导出到文件")
    options = parser.parse_args()
    ports = set(options.port)

    captures = [options.capture] if options.capture else sorted(CAPTURE_DIR.glob("*.pcapng"))
    if not captures:
        raise SystemExit("没有抓包：%s" % CAPTURE_DIR)

    total = collections.Counter()
    report = {"captures": [], "ports": sorted(ports)}
    for capture in captures:
        frames_seen, census = census_capture(capture, ports)
        if frames_seen == 0:
            continue
        total.update(census)
        hits = {opcode_name(op): census[op] for op in sorted(options.op) if census[op]}
        print("%s: frames=%d %s" % (capture.name, frames_seen, hits or "(no watched opcodes)"),
              flush=True)
        report["captures"].append({"capture": capture.name, "frames": frames_seen,
                                   "watched": {str(op): census[op] for op in sorted(options.op)}})

    print()
    print("=== 合计（含 C2S/S2C，端口 %s）===" % ",".join(str(port) for port in sorted(ports)))
    for opcode in sorted(options.op):
        print("  %d %s: %d" % (opcode, opcode_name(opcode), total[opcode]))
    report["watched_total"] = {str(op): total[op] for op in sorted(options.op)}

    if options.json:
        options.json.parent.mkdir(parents=True, exist_ok=True)
        options.json.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
        print("导出：%s" % options.json)

    seen_absent = {op: total[op] for op in options.absent if total[op]}
    if seen_absent:
        print("FAIL 这些 opcode 应当没有线上样本，却抓到了：%s"
              % {opcode_name(op): count for op, count in sorted(seen_absent.items())})
        raise SystemExit(1)
    if options.absent:
        print("PASS %s 在线上抓包里均为 0 条"
              % ",".join(opcode_name(op) for op in sorted(options.absent)))


if __name__ == "__main__":
    main()
