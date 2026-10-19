import { createApp } from "vue";
import ElementPlus from "element-plus";
import zhCn from "element-plus/es/locale/lang/zh-cn";
import "element-plus/dist/index.css";
import { Upload, UploadFilled } from "@element-plus/icons-vue";
import App from "./App.vue";
import "./style.css";

const app = createApp(App);
// 只注册界面实际用到的图标（全量注册会把 300+ 个图标全部打进产物）
for (const [name, comp] of Object.entries({ Upload, UploadFilled })) {
  app.component(name, comp);
}
app.use(ElementPlus, { locale: zhCn });
app.mount("#app");