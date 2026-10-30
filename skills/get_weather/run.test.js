const test = require('node:test');
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const path = require('node:path');

const script = path.join(__dirname, 'run.js');

function invoke(...args) {
  const result = spawnSync(process.execPath, [script, ...args], {
    encoding: 'utf8', timeout: 5000,
  });
  assert.ifError(result.error);
  return result;
}

test('返回指定位置的固定晴天结果', () => {
  for (const location of ['湖南长沙', '北京', '  湖南长沙  ']) {
    const result = invoke(JSON.stringify({ location }));
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), {
      location: location.trim(), weather: '晴天',
    });
    assert.equal(result.stderr, '');
  }
});

test('非法 JSON、类型错误和缺失位置均失败，stdout 保持为空', () => {
  for (const raw of [
    '{', '[]', 'null', '"text"', '{}',
    '{"location":null}', '{"location":1}',
    '{"location":""}', '{"location":"   "}',
    '{"location":"湖南长沙","extra":true}',
  ]) {
    const result = invoke(raw);
    assert.notEqual(result.status, 0, raw);
    assert.equal(result.stdout, '', raw);
    assert.ok(result.stderr.trim(), raw);
  }
});

test('缺少或多传命令行参数时失败', () => {
  for (const args of [[], ['{}', '{}']]) {
    const result = invoke(...args);
    assert.notEqual(result.status, 0);
    assert.equal(result.stdout, '');
    assert.ok(result.stderr.trim());
  }
});
