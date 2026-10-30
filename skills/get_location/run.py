import json
import sys


def main():
    try:
        if len(sys.argv) > 2:
            raise ValueError("只接受一个 JSON 参数")
        raw_args = sys.argv[1] if len(sys.argv) == 2 else "{}"
        args = json.loads(raw_args)
        if not isinstance(args, dict):
            raise ValueError("参数必须是 JSON 对象")
        if args:
            raise ValueError("get_location 不接受额外字段，请传 {}")
    except (ValueError, TypeError) as exc:
        print("get_location: {}".format(exc), file=sys.stderr)
        return 1

    # 学习用固定数据，不读取 IP、GPS 或设备权限。
    print(json.dumps({"location": "湖南长沙"}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())