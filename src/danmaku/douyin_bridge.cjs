'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const readline = require('node:readline');

function envBool(name, fallback) {
  const value = process.env[name];
  if (value === undefined || value === '') {
    return fallback;
  }
  return value === '1' || value === 'true';
}

function log(level, message) {
  process.stderr.write(`${JSON.stringify({ level, message })}\n`);
}

function patchBundle(bundlePath) {
  const source = fs.readFileSync(bundlePath, 'utf8');
  if (!source.includes('class DouYinDanmaClient')) {
    throw new Error('biliLive-tools 包内未找到 DouYinDanmaClient');
  }
  const hash = crypto.createHash('sha1').update(source).digest('hex').slice(0, 12);
  const patchedPath = path.join(path.dirname(bundlePath), `.bililive-go-douyin-danma-${hash}.cjs`);
  if (!fs.existsSync(patchedPath)) {
    fs.writeFileSync(patchedPath, `${source}\nexports.DouYinDanmaClient = DouYinDanmaClient;\n`);
  }
  return patchedPath;
}

function cleanText(text) {
  return String(text || '').replace(/\r/g, '').replace(/\n/g, '').trim();
}

function finiteNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}

function normalizeTimestamp(value) {
  const number = finiteNumber(value);
  if (number <= 0) {
    return Date.now();
  }
  if (number > 99999999999999) {
    return Math.trunc(number / 1000000);
  }
  if (number > 9999999999) {
    return Math.trunc(number);
  }
  return Math.trunc(number * 1000);
}

const bundlePath = process.env.BILILIVE_DOUYIN_BUNDLE;
const roomId = process.env.BILILIVE_DOUYIN_ROOM_ID;
const cookie = process.env.BILILIVE_DOUYIN_COOKIE || '';
const useServerTimestamp = envBool('BILILIVE_DOUYIN_USE_SERVER_TIMESTAMP', true);
const saveGift = envBool('BILILIVE_DOUYIN_SAVE_GIFT', false);

if (!bundlePath || !roomId) {
  log('error', '缺少抖音弹幕桥接参数');
  process.exit(1);
}

const giftCache = new Map();
const giftDelay = 5000;
let shuttingDown = false;
let restarting = false;
let fallbackTried = false;
let client;
let activeCookie = cookie;
let reconnectTimer;

function writeMessage(message) {
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

function flushGifts() {
  for (const cached of giftCache.values()) {
    clearTimeout(cached.timer);
    writeMessage(cached.message);
  }
  giftCache.clear();
}

function commentTimestamp(message, fallbackMode) {
  if (!useServerTimestamp) {
    return Date.now();
  }
  if (fallbackMode === 'nano') {
    return normalizeTimestamp(message.eventTime);
  }
  if (message.eventTime) {
    return Math.trunc(finiteNumber(message.eventTime) * 1000);
  }
  return Date.now();
}

function emitComment(message, color, fallbackMode) {
  const text = cleanText(message.content);
  if (!text) {
    return;
  }
  writeMessage({
    type: 'comment',
    timestamp: commentTimestamp(message, fallbackMode),
    text,
    color,
    mode: 1,
    sender: {
      uid: String(message?.user?.id || ''),
      name: String(message?.user?.nickName || ''),
    },
  });
}

const { DouYinDanmaClient } = require(patchBundle(bundlePath));

function scheduleReconnect(reason) {
  if (shuttingDown || restarting) {
    return;
  }
  if (reconnectTimer) {
    return;
  }
  log('warn', `${reason}，2 秒后重连`);
  reconnectTimer = setTimeout(() => {
    reconnectTimer = undefined;
    if (!shuttingDown && !restarting) {
      startClient(activeCookie);
    }
  }, 2000);
}

function restartWithoutConfiguredCookie(instance) {
  if (instance !== client || fallbackTried || !cookie) {
    return false;
  }
  fallbackTried = true;
  restarting = true;
  activeCookie = '';
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = undefined;
  }
  log('warn', '使用配置 cookie 连接抖音弹幕失败，改用临时 cookie 重试');
  try {
    client?.close();
  } catch {}
  setTimeout(() => {
    startClient(activeCookie);
    restarting = false;
  }, 300);
  return true;
}

function bindClientEvents(instance) {
  instance.on('chat', (message) => {
    emitComment(message, '#ffffff', 'second');
  });

  instance.on('privilegeScreenChat', (message) => {
    emitComment(message, '#e0c39c', 'now');
  });

  instance.on('screenChat', (message) => {
    emitComment(message, '#d7f6fc', 'nano');
  });

  instance.on('gift', (message) => {
    if (!saveGift) {
      return;
    }
    const userID = String(message?.user?.id || '');
    const giftID = String(message?.giftId || message?.gift?.id || message?.gift?.name || '');
    const groupID = `${message?.groupId || ''}_${userID}_${giftID}`;
    const output = {
      type: 'give_gift',
      timestamp: useServerTimestamp ? normalizeTimestamp(message?.common?.createTime) : Date.now(),
      name: String(message?.gift?.name || ''),
      count: Math.trunc(finiteNumber(message?.totalCount) || 1),
      price: finiteNumber(message?.gift?.diamondCount) / 10,
      color: '#ffffff',
      sender: {
        uid: userID,
        name: String(message?.user?.nickName || 'unknown'),
      },
    };
    const existing = giftCache.get(groupID);
    if (existing) {
      clearTimeout(existing.timer);
    }
    const timer = setTimeout(() => {
      const cached = giftCache.get(groupID);
      if (!cached) {
        return;
      }
      writeMessage(cached.message);
      giftCache.delete(groupID);
    }, giftDelay);
    giftCache.set(groupID, { message: output, timer });
  });

  instance.on('init', () => log('debug', `抖音弹幕连接初始化: ${roomId}`));
  instance.on('open', () => log('info', `抖音弹幕连接已打开: ${roomId}`));
  instance.on('close', () => {
    if (instance !== client) {
      return;
    }
    log('debug', `抖音弹幕连接已关闭: ${roomId}`);
    scheduleReconnect('抖音弹幕连接已关闭');
  });
  instance.on('error', (error) => {
    if (instance !== client) {
      return;
    }
    const message = error?.stack || error?.message || String(error);
    log('warn', `抖音弹幕连接异常: ${message}`);
    if (!restartWithoutConfiguredCookie(instance)) {
      scheduleReconnect('抖音弹幕连接异常');
    }
  });
}

function startClient(cookieValue) {
  activeCookie = cookieValue;
  client = new DouYinDanmaClient(roomId, {
    cookie: cookieValue,
    autoReconnect: 0,
    reconnectInterval: 10000,
  });
  bindClientEvents(client);
  Promise.resolve(client.connect()).catch((error) => {
    log('error', `启动抖音弹幕连接失败: ${error?.stack || error?.message || String(error)}`);
    scheduleReconnect('启动抖音弹幕连接失败');
  });
}

function shutdown() {
  if (shuttingDown) {
    return;
  }
  shuttingDown = true;
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = undefined;
  }
  flushGifts();
  try {
    client?.close();
  } catch (error) {
    log('warn', `关闭抖音弹幕连接失败: ${error?.message || String(error)}`);
  }
  setTimeout(() => process.exit(0), 200);
}

readline.createInterface({ input: process.stdin }).on('line', (line) => {
  if (line.trim() === 'close') {
    shutdown();
  }
});

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);

startClient(cookie);
