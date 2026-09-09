import { h as o } from "./vue.runtime.esm-bundler-CtrlPn7D.js";
const n = {
  name: "ExtActionDemo",
  render() {
    return o(
      "button",
      {
        style: {
          width: "100%",
          background: "none",
          border: "1px solid transparent",
          borderRadius: "8px",
          color: "#565d65",
          cursor: "pointer",
          padding: "4px 8px",
          fontSize: "12px",
          textAlign: "left"
        },
        onClick: () => console.log("[extension-demo] 侧栏动作被点击")
      },
      "示例动作(侧栏注入)"
    );
  }
};
export {
  n as default
};
