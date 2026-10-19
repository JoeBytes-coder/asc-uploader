<template>
  <div class="page">
    <header class="hero">
      <div class="hero-badge"> App Store Connect API </div>
      <h1>iOS 安装包上传工具</h1>
      <p>无需 Xcode / Transporter · Windows 本机运行 · 密钥仅在本机与 Apple 之间传输</p>
    </header>

    <!-- ① API 密钥与 App -->
    <el-card shadow="never">
      <template #header><div class="card-title"><span class="step">1</span> API 密钥与 App</div></template>
      <el-form label-position="top" @submit.prevent>
        <el-row :gutter="14">
          <el-col :span="24">
            <el-form-item label="密钥类型">
              <el-radio-group v-model="conn.keyType">
                <el-radio value="team">团队密钥</el-radio>
                <el-radio value="individual">个人密钥</el-radio>
              </el-radio-group>
              <div class="keytype-tip">{{ keyTypeTip }}</div>
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="私钥文件（.p8）">
              <el-input v-model="conn.keyPath" placeholder="本机路径，如 C:\keys\AuthKey_XXX.p8；也可点右侧按钮选择文件">
                <template #append>
                  <el-button :disabled="conn.testing" @click="pickKey">选择 .p8 文件…</el-button>
                </template>
              </el-input>
              <div v-if="conn.keyUploadedName" style="margin-top: 6px">
                <el-tag type="success" size="small">已上传：{{ conn.keyUploadedName }}</el-tag>
              </div>
            </el-form-item>
          </el-col>
          <el-col :xs="24" :sm="12">
            <el-form-item label="Key ID"><el-input v-model="conn.keyId" placeholder="AuthKey_ 后面那串" clearable /></el-form-item>
          </el-col>
          <el-col v-if="conn.keyType === 'team'" :xs="24" :sm="12">
            <el-form-item label="Issuer ID"><el-input v-model="conn.issuerId" placeholder="密钥页顶部的 UUID（团队共享）" clearable /></el-form-item>
          </el-col>
          <el-col v-else :xs="24" :sm="12">
            <el-form-item label="Issuer ID">
              <el-input model-value="" placeholder="个人密钥不需要 Issuer ID" disabled />
            </el-form-item>
          </el-col>
          <el-col :span="24">
            <el-form-item label="App（点下方按钮获取列表后选择）">
              <el-select v-model="conn.appId" filterable placeholder="— 请选择 App —" style="width: 100%">
                <el-option v-for="a in conn.apps" :key="a.id" :label="`${a.name}（${a.bundleId}）`" :value="a.id" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
        <el-button type="primary" plain :loading="conn.testing" :disabled="!readyForApps" @click="testConn">
          {{ conn.testing ? "连接中…" : "测试连接 / 获取 App 列表" }}
        </el-button>
        <el-alert v-if="conn.testError" :title="conn.testError" type="error" show-icon :closable="false" style="margin-top: 14px" />
        <el-collapse style="margin-top: 8px">
          <el-collapse-item title="高级设置" name="adv">
            <el-form-item label="API 地址（默认官方；联调测试时可修改）">
              <el-input v-model="conn.apiBase" placeholder="https://api.appstoreconnect.apple.com" clearable />
            </el-form-item>
          </el-collapse-item>
        </el-collapse>
      </el-form>
    </el-card>

    <!-- ② 安装包 -->
    <el-card shadow="never">
      <template #header><div class="card-title"><span class="step">2</span> 安装包（.ipa / .pkg）</div></template>
      <el-upload drag :show-file-list="false" :http-request="ipaUploadRequest" accept=".ipa,.pkg" style="width: 100%">
        <div style="padding: 16px 0">
          <el-icon :size="40" color="#c0c4cc"><UploadFilled /></el-icon>
          <div class="el-upload__text">把 .ipa / .pkg 拖到这里，或 <em>点击选择</em></div>
        </div>
      </el-upload>
      <el-progress v-if="pkg.localPct > 0 && pkg.localPct < 100" :percentage="pkg.localPct" :stroke-width="6" style="margin-top: 8px" />
      <el-descriptions v-if="pkg.fileName" class="pkg-desc" :column="4" border size="small" style="margin-top: 14px">
        <el-descriptions-item label="文件">{{ pkg.fileName }}（{{ fmtBytes(pkg.size) }}）</el-descriptions-item>
        <el-descriptions-item label="App">{{ pkg.info?.name || "-" }}</el-descriptions-item>
        <el-descriptions-item label="Bundle ID">{{ pkg.info?.bundleId || "-" }}</el-descriptions-item>
        <el-descriptions-item label="版本 / 构建">{{ pkg.info?.version || "-" }}（{{ pkg.info?.build || "-" }}）</el-descriptions-item>
      </el-descriptions>
      <el-alert v-if="pkg.error" :title="pkg.error" type="warning" show-icon :closable="false" style="margin-top: 12px" />
      <el-form label-position="top" style="margin-top: 14px">
        <el-form-item label="或使用本机路径（安装包就在这台电脑上时最方便）">
          <el-input v-model="pkg.pathInput" placeholder="D:\build\MyApp.ipa" @change="usePath" clearable />
        </el-form-item>
        <el-row :gutter="14">
          <el-col :xs="24" :sm="8">
            <el-form-item label="版本号（覆盖）"><el-input v-model="pkg.version" placeholder="默认读取自包内" clearable /></el-form-item>
          </el-col>
          <el-col :xs="24" :sm="8">
            <el-form-item label="构建号（覆盖）"><el-input v-model="pkg.build" placeholder="默认读取自包内" clearable /></el-form-item>
          </el-col>
          <el-col :xs="24" :sm="8">
            <el-form-item label="平台">
              <el-select v-model="pkg.platform" style="width: 100%">
                <el-option label="iOS" value="IOS" />
                <el-option label="macOS (.pkg)" value="MAC_OS" />
                <el-option label="tvOS" value="TV_OS" />
                <el-option label="visionOS" value="VISION_OS" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
    </el-card>

    <!-- ③ 上传 -->
    <el-card shadow="never">
      <template #header><div class="card-title"><span class="step">3</span> 上传</div></template>
      <div style="display: flex; gap: 12px; flex-wrap: wrap; margin-bottom: 18px">
        <el-button type="primary" size="large" :disabled="!canStart" @click="start(true)">
          <el-icon style="margin-right: 6px"><Upload /></el-icon>上传并等待处理结果
        </el-button>
        <el-button size="large" :disabled="!canStart" @click="start(false)">仅上传字节（不等待）</el-button>
        <el-button v-if="running" type="danger" size="large" plain @click="cancel">取消</el-button>
      </div>
      <template v-if="job">
        <el-steps :active="stepIdx" align-center finish-status="success" style="margin-bottom: 18px">
          <el-step v-for="s in stepList" :key="s.key" :title="s.name" />
        </el-steps>
        <el-progress v-if="job.bytesTotal > 0" :percentage="pct" :stroke-width="18" :text-inside="true" striped striped-flow />
        <div class="statrow" style="margin-top: 10px">
          <span v-if="job.bytesTotal > 0">{{ fmtBytes(job.bytesDone) }} / {{ fmtBytes(job.bytesTotal) }}（{{ pct }}%）</span>
          <span v-if="job.speed">{{ job.speed }}</span>
          <el-tag :type="stateTagType" effect="dark">{{ stateText }}</el-tag>
        </div>
        <el-alert v-if="job.state === 'done' && job.result" type="success" show-icon :closable="false" style="margin-top: 14px">
          <template #title>上传完成，Apple 已接收该构建！</template>
          <div>会话 ID：{{ job.result.buildUploadId }}</div>
          <div v-if="job.result.buildId">Build ID：<b>{{ job.result.buildId }}</b></div>
          <div style="color: var(--muted); font-size: 12px; margin-top: 4px">请到 App Store Connect（TestFlight / 构建版本页）查看后续处理。</div>
        </el-alert>
        <el-alert v-if="job.state === 'failed'" :title="job.error" type="error" show-icon :closable="false" style="margin-top: 14px" />
        <el-alert v-if="job.state === 'canceled'" title="已取消。" type="warning" show-icon :closable="false" style="margin-top: 14px" />
        <pre class="logs" style="margin-top: 14px">{{ job.logs.join("\n") }}</pre>
      </template>
      <el-empty v-else description="填写上方信息后点击「上传」开始" :image-size="80" />
    </el-card>

    <footer>密钥与安装包仅在本机与 Apple 官方服务器之间传输 · JWT 现场签发（最长 18 分钟有效）</footer>
    <input ref="keyInput" type="file" accept=".p8" hidden @change="onKeyFile" />
  </div>
</template>

<script setup>
import { reactive, ref, computed, onMounted, onUnmounted, nextTick } from "vue";
import { ElMessage } from "element-plus";

const conn = reactive({
  keyPath: "", keyUploadedName: "", keyId: "", issuerId: "", apiBase: "",
  keyType: "team", // team（团队密钥）/ individual（个人密钥）——协议相同，凭据来源不同
  apps: [], appId: "", testing: false, testError: "",
});
const pkg = reactive({
  path: "", fileName: "", size: 0, info: null, error: "",
  localPct: 0, version: "", build: "", platform: "IOS", pathInput: "",
});
const job = ref(null);
const keyInput = ref(null);
const logsBox = ref(null);
let pollTimer = null;
let notifiedDone = false;

const keyTypeTip = computed(() => conn.keyType === "individual"
  ? "个人密钥：右上角用户名 → 编辑个人资料 → Individual API Key 生成（权限＝你本人角色）。按官方文档，个人密钥不使用 Issuer ID（认证 token 用 sub=user 代替），只需 .p8 与 Key ID；每人同时只能有一把活跃个人密钥。"
  : "团队密钥：用户和访问 → 集成 → App Store Connect API → Team Keys 创建（需账户持有人/管理员）。Issuer ID 全团队共享，在页面顶部。");
const running = computed(() => !!job.value && ["prepare", "session", "reserve", "upload", "commit", "wait"].includes(job.value.state));
const readyForApps = computed(() => !!(conn.keyPath && conn.keyId && (conn.keyType === "individual" || conn.issuerId)));
const canStart = computed(() => readyForApps.value && conn.appId && pkg.path && !running.value);
const stepList = [
  { name: "解析安装包", key: "prepare" },
  { name: "创建上传会话", key: "session" },
  { name: "预留文件位", key: "reserve" },
  { name: "上传字节", key: "upload" },
  { name: "提交文件", key: "commit" },
  { name: "等待处理", key: "wait" },
];
const stepIdx = computed(() => {
  if (job.value?.state === "done") return stepList.length;
  const i = stepList.findIndex(s => s.key === job.value?.state);
  return i;
});
const pct = computed(() => (job.value && job.value.bytesTotal ? Math.min(100, Math.floor((job.value.bytesDone / job.value.bytesTotal) * 100)) : 0));
const stateText = computed(() => ({
  prepare: "准备中", session: "创建会话", reserve: "预留文件位", upload: "上传字节", commit: "提交文件",
  wait: "等待 Apple 处理", done: "完成", failed: "失败", canceled: "已取消",
}[job.value?.state] || job.value?.state || ""));
const stateTagType = computed(() => ({ done: "success", failed: "danger", canceled: "warning", upload: "primary", wait: "primary" }[job.value?.state] || "info"));

function fmtBytes(n) {
  if (n === 0) return "0 B";
  if (!n && n !== 0) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 ** 2) return (n / 1024).toFixed(1) + " KB";
  if (n < 1024 ** 3) return (n / 1024 ** 2).toFixed(1) + " MB";
  return (n / 1024 ** 3).toFixed(2) + " GB";
}

function saveLocal() {
  try { localStorage.setItem("asc-conn", JSON.stringify({ keyPath: conn.keyPath, keyId: conn.keyId, issuerId: conn.issuerId, appId: conn.appId, keyType: conn.keyType })); } catch (e) { /* 忽略 */ }
}
function loadLocal() {
  try {
    const s = JSON.parse(localStorage.getItem("asc-conn") || "{}");
    conn.keyPath = s.keyPath || ""; conn.keyId = s.keyId || ""; conn.issuerId = s.issuerId || ""; conn.appId = s.appId || "";
    conn.keyType = s.keyType === "individual" ? "individual" : "team";
  } catch (e) { /* 忽略 */ }
}

// API 地址：原生窗口模式下由 api-config.js 注入本机服务地址；浏览器/开发模式为同源
const API_BASE = (typeof window !== "undefined" && window.__ASC_API_BASE) || "";

async function api(path, body) {
  const res = await fetch(API_BASE + path, body ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : undefined);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || "HTTP " + res.status);
  return data;
}

/** XHR 上传文件到本机服务（带进度回调） */
function uploadFile(url, file, onPct) {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    fd.append("file", file, file.name);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", (API_BASE || "") + url + "?name=" + encodeURIComponent(file.name));
    xhr.upload.onprogress = e => { if (e.lengthComputable) onPct(Math.floor((e.loaded / e.total) * 100)); };
    xhr.onload = () => {
      try {
        const data = JSON.parse(xhr.responseText);
        if (xhr.status >= 200 && xhr.status < 300) resolve(data);
        else reject(new Error(data.error || "HTTP " + xhr.status));
      } catch (e) { reject(new Error("响应解析失败")); }
    };
    xhr.onerror = () => reject(new Error("网络错误（本机服务未运行？）"));
    xhr.send(fd);
  });
}

function pickKey() { keyInput.value?.click(); }

async function onKeyFile(e) {
  const f = e.target.files?.[0];
  if (!f) return;
  conn.testError = "";
  try {
    const r = await uploadFile("/api/key-file", f, () => {});
    conn.keyPath = r.path;
    conn.keyUploadedName = f.name;
    saveLocal();
    ElMessage.success("私钥已上传");
  } catch (err) { conn.testError = "私钥上传失败：" + err.message; }
}

async function sendIpa(f, onPct) {
  pkg.error = ""; pkg.info = null; pkg.fileName = ""; pkg.size = 0; pkg.localPct = 1;
  try {
    const r = await uploadFile("/api/ipa", f, p => { pkg.localPct = Math.max(1, p - 1); onPct && onPct(p); });
    pkg.path = r.path; pkg.fileName = r.fileName; pkg.size = r.size; pkg.info = r.info;
    pkg.version = r.info?.version || ""; pkg.build = r.info?.build || "";
    if (r.warn) pkg.error = r.warn;
    pkg.localPct = 100;
    ElMessage.success("安装包已就绪");
  } catch (err) { pkg.error = "安装包处理失败：" + err.message; pkg.localPct = 0; throw err; }
}

/** el-upload 的自定义上传请求 */
async function ipaUploadRequest({ file, onProgress, onSuccess, onError }) {
  try {
    const r = await sendIpa(file, p => onProgress({ percent: p }));
    onSuccess(r);
  } catch (e) { onError(e); }
}

async function usePath() {
  const p = pkg.pathInput.trim();
  if (!p) return;
  pkg.error = "";
  try {
    const r = await api("/api/parse-path", { path: p });
    pkg.path = r.path; pkg.fileName = r.fileName; pkg.size = r.size; pkg.info = r.info;
    pkg.version = r.info?.version || ""; pkg.build = r.info?.build || "";
    if (r.warn) pkg.error = r.warn;
  } catch (err) { pkg.error = "读取路径失败：" + err.message; }
}

async function testConn() {
  conn.testing = true; conn.testError = ""; conn.apps = [];
  try {
    const r = await api("/api/apps", { keyPath: conn.keyPath, keyId: conn.keyId, issuerId: conn.issuerId, apiBase: conn.apiBase, keyType: conn.keyType });
    conn.apps = r.apps || [];
    if (!conn.apps.length) conn.testError = "连接成功，但该密钥没有可访问的 App（检查角色与访问范围）。";
    else ElMessage.success(`连接成功，获取到 ${conn.apps.length} 个 App`);
    saveLocal();
  } catch (err) { conn.testError = err.message; }
  finally { conn.testing = false; }
}

async function start(wait) {
  saveLocal();
  pkg.error = "";
  notifiedDone = false;
  try {
    await api("/api/start", {
      keyPath: conn.keyPath, keyId: conn.keyId, issuerId: conn.issuerId, apiBase: conn.apiBase, keyType: conn.keyType,
      appId: conn.appId, ipaPath: pkg.path, wait,
      version: pkg.version || "", build: pkg.build || "", platform: pkg.platform,
    });
    job.value = await api("/api/job");
    startPoll();
  } catch (err) { pkg.error = "启动上传失败：" + err.message; }
}

async function cancel() {
  try { await api("/api/cancel", {}); ElMessage.info("已请求取消"); } catch (e) { /* 忽略 */ }
}

function startPoll() {
  stopPoll();
  pollTimer = setInterval(async () => {
    try {
      const prev = job.value;
      job.value = await api("/api/job");
      if (job.value.logs?.length) nextTick(() => { const el = logsBox.value; if (el) el.scrollTop = el.scrollHeight; });
      if (job.value.state === "done" && !prev?.finished && !notifiedDone) {
        notifiedDone = true;
        ElMessage.success("上传完成，Apple 已接收该构建");
      }
      if (!running.value) stopPoll();
    } catch (e) { stopPoll(); }
  }, 600);
}
function stopPoll() { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } }

onMounted(async () => {
  loadLocal();
  try { job.value = await api("/api/job"); if (running.value) startPoll(); } catch (e) { /* 忽略 */ }
});
onUnmounted(stopPoll);
</script>
