function main() {
  try {
    if (process.argv.length !== 3) {
      throw new Error('需要一个 JSON 参数');
    }
    const args = JSON.parse(process.argv[2]);
    if (args === null || typeof args !== 'object' || Array.isArray(args)) {
      throw new Error('参数必须是 JSON 对象');
    }
    if (Object.keys(args).some((key) => key !== 'location')) {
      throw new Error('只接受 location 字段');
    }
    if (typeof args.location !== 'string' || args.location.trim() === '') {
      throw new Error('location 必须是非空字符串');
    }

    // 学习用固定数据，不请求天气 API。
    const result = { location: args.location.trim(), weather: '晴天' };
    process.stdout.write(JSON.stringify(result) + '\n');
    return 0;
  } catch (error) {
    process.stderr.write(`get_weather: ${error.message}\n`);
    return 1;
  }
}

process.exitCode = main();