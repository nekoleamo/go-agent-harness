/**
* @vue/shared v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
const K = process.env.NODE_ENV !== "production" ? Object.freeze({}) : {};
process.env.NODE_ENV !== "production" && Object.freeze([]);
const ne = () => {
}, we = (e) => e.charCodeAt(0) === 111 && e.charCodeAt(1) === 110 && // uppercase letter
(e.charCodeAt(2) > 122 || e.charCodeAt(2) < 97), be = (e) => e.startsWith("onUpdate:"), H = Object.assign, m = Array.isArray, b = (e) => typeof e == "function", y = (e) => typeof e == "string", Oe = (e) => typeof e == "symbol", N = (e) => e !== null && typeof e == "object";
let ee;
const U = () => ee || (ee = typeof globalThis < "u" ? globalThis : typeof self < "u" ? self : typeof window < "u" ? window : typeof global < "u" ? global : {});
function Y(e) {
  if (m(e)) {
    const t = {};
    for (let n = 0; n < e.length; n++) {
      const o = e[n], s = y(o) ? ke(o) : Y(o);
      if (s)
        for (const r in s)
          t[r] = s[r];
    }
    return t;
  } else if (y(e) || N(e))
    return e;
}
const Se = /;(?![^(]*\))/g, Ve = /:([^]+)/, Ce = /\/\*[^]*?\*\//g;
function ke(e) {
  const t = {};
  return e.replace(Ce, "").split(Se).forEach((n) => {
    if (n) {
      const o = n.split(Ve);
      o.length > 1 && (t[o[0].trim()] = o[1].trim());
    }
  }), t;
}
function G(e) {
  let t = "";
  if (y(e))
    t = e;
  else if (m(e))
    for (let n = 0; n < e.length; n++) {
      const o = G(e[n]);
      o && (t += o + " ");
    }
  else if (N(e))
    for (const n in e)
      e[n] && (t += n + " ");
  return t.trim();
}
/**
* @vue/reactivity v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
process.env.NODE_ENV;
process.env.NODE_ENV;
process.env.NODE_ENV;
new Set(
  /* @__PURE__ */ Object.getOwnPropertyNames(Symbol).filter((e) => e !== "arguments" && e !== "caller").map((e) => Symbol[e]).filter(Oe)
);
// @__NO_SIDE_EFFECTS__
function oe(e) {
  return /* @__PURE__ */ J(e) ? /* @__PURE__ */ oe(e.__v_raw) : !!(e && e.__v_isReactive);
}
// @__NO_SIDE_EFFECTS__
function J(e) {
  return !!(e && e.__v_isReadonly);
}
// @__NO_SIDE_EFFECTS__
function v(e) {
  return !!(e && e.__v_isShallow);
}
// @__NO_SIDE_EFFECTS__
function W(e) {
  return e ? !!e.__v_raw : !1;
}
// @__NO_SIDE_EFFECTS__
function O(e) {
  const t = e && e.__v_raw;
  return t ? /* @__PURE__ */ O(t) : e;
}
// @__NO_SIDE_EFFECTS__
function Q(e) {
  return e ? e.__v_isRef === !0 : !1;
}
/**
* @vue/runtime-core v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
const S = [];
function Re(e) {
  S.push(e);
}
function Te() {
  S.pop();
}
let L = !1;
function w(e, ...t) {
  if (L) return;
  L = !0;
  const n = S.length ? S[S.length - 1].component : null, o = n && n.appContext.config.warnHandler, s = De();
  if (o)
    X(
      o,
      n,
      11,
      [
        // eslint-disable-next-line no-restricted-syntax
        e + t.map((r) => {
          var l, c;
          return (c = (l = r.toString) == null ? void 0 : l.call(r)) != null ? c : JSON.stringify(r);
        }).join(""),
        n && n.proxy,
        s.map(
          ({ vnode: r }) => `at <${Ee(n, r.type)}>`
        ).join(`
`),
        s
      ]
    );
  else {
    const r = [`[Vue warn]: ${e}`, ...t];
    s.length && r.push(`
`, ...xe(s)), console.warn(...r);
  }
  L = !1;
}
function De() {
  let e = S[S.length - 1];
  if (!e)
    return [];
  const t = [];
  for (; e; ) {
    const n = t[0];
    n && n.vnode === e ? n.recurseCount++ : t.push({
      vnode: e,
      recurseCount: 0
    });
    const o = e.component && e.component.parent;
    e = o && o.vnode;
  }
  return t;
}
function xe(e) {
  const t = [];
  return e.forEach((n, o) => {
    t.push(...o === 0 ? [] : [`
`], ...Fe(n));
  }), t;
}
function Fe({ vnode: e, recurseCount: t }) {
  const n = t > 0 ? `... (${t} recursive calls)` : "", o = e.component ? e.component.parent == null : !1, s = ` at <${Ee(
    e.component,
    e.type,
    o
  )}`, r = ">" + n;
  return e.props ? [s, ...Ie(e.props), r] : [s + r];
}
function Ie(e) {
  const t = [], n = Object.keys(e);
  return n.slice(0, 3).forEach((o) => {
    t.push(...re(o, e[o]));
  }), n.length > 3 && t.push(" ..."), t;
}
function re(e, t, n) {
  return y(t) ? (t = JSON.stringify(t), n ? t : [`${e}=${t}`]) : typeof t == "number" || typeof t == "boolean" || t == null ? n ? t : [`${e}=${t}`] : /* @__PURE__ */ Q(t) ? (t = re(e, /* @__PURE__ */ O(t.value), !0), n ? t : [`${e}=Ref<`, t, ">"]) : b(t) ? [`${e}=fn${t.name ? `<${t.name}>` : ""}`] : (t = /* @__PURE__ */ O(t), n ? t : [`${e}=`, t]);
}
const se = {
  sp: "serverPrefetch hook",
  bc: "beforeCreate hook",
  c: "created hook",
  bm: "beforeMount hook",
  m: "mounted hook",
  bu: "beforeUpdate hook",
  u: "updated",
  bum: "beforeUnmount hook",
  um: "unmounted hook",
  a: "activated hook",
  da: "deactivated hook",
  ec: "errorCaptured hook",
  rtc: "renderTracked hook",
  rtg: "renderTriggered hook",
  0: "setup function",
  1: "render function",
  2: "watcher getter",
  3: "watcher callback",
  4: "watcher cleanup function",
  5: "native event handler",
  6: "component event handler",
  7: "vnode hook",
  8: "directive hook",
  9: "transition hook",
  10: "app errorHandler",
  11: "app warnHandler",
  12: "ref function",
  13: "async component loader",
  14: "scheduler flush",
  15: "component update",
  16: "app unmount cleanup function"
};
function X(e, t, n, o) {
  try {
    return o ? e(...o) : e();
  } catch (s) {
    ie(s, t, n);
  }
}
function ie(e, t, n, o = !0) {
  const s = t ? t.vnode : null, { errorHandler: r, throwUnhandledErrorInProduction: l } = t && t.appContext.config || K;
  if (t) {
    let c = t.parent;
    const a = t.proxy, f = process.env.NODE_ENV !== "production" ? se[n] : `https://vuejs.org/error-reference/#runtime-${n}`;
    for (; c; ) {
      const _ = c.ec;
      if (_) {
        for (let i = 0; i < _.length; i++)
          if (_[i](e, a, f) === !1)
            return;
      }
      c = c.parent;
    }
    if (r) {
      X(r, null, 10, [
        e,
        a,
        f
      ]);
      return;
    }
  }
  $e(e, n, s, o, l);
}
function $e(e, t, n, o = !0, s = !1) {
  if (process.env.NODE_ENV !== "production") {
    const r = se[t];
    if (n && Re(n), w(`Unhandled error${r ? ` during execution of ${r}` : ""}`), n && Te(), o)
      throw e;
    console.error(e);
  } else {
    if (s)
      throw e;
    console.error(e);
  }
}
const d = [];
let g = -1;
const k = [];
let E = null, V = 0;
const Ae = /* @__PURE__ */ Promise.resolve();
let q = null;
const Pe = 100;
function Me(e) {
  let t = g + 1, n = d.length;
  for (; t < n; ) {
    const o = t + n >>> 1, s = d[o], r = D(s);
    r < e || r === e && s.flags & 2 ? t = o + 1 : n = o;
  }
  return t;
}
function He(e) {
  if (!(e.flags & 1)) {
    const t = D(e), n = d[d.length - 1];
    !n || // fast path when the job id is larger than the tail
    !(e.flags & 2) && t >= D(n) ? d.push(e) : d.splice(Me(t), 0, e), e.flags |= 1, le();
  }
}
function le() {
  q || (q = Ae.then(ce));
}
function Ue(e) {
  if (!m(e))
    E && e.id === -1 ? E.splice(V + 1, 0, e) : e.flags & 1 || (k.push(e), e.flags |= 1);
  else
    for (let t = 0; t < e.length; t++)
      k.push(e[t]);
  le();
}
function ve(e) {
  if (k.length) {
    const t = [...new Set(k)].sort(
      (n, o) => D(n) - D(o)
    );
    if (k.length = 0, E) {
      for (let n = 0; n < t.length; n++)
        E.push(t[n]);
      return;
    }
    for (E = t, process.env.NODE_ENV !== "production" && (e = e || /* @__PURE__ */ new Map()), V = 0; V < E.length; V++) {
      const n = E[V];
      process.env.NODE_ENV !== "production" && ae(e, n) || (n.flags & 4 && (n.flags &= -2), n.flags & 8 || n(), n.flags &= -2);
    }
    E = null, V = 0;
  }
}
const D = (e) => e.id == null ? e.flags & 2 ? -1 : 1 / 0 : e.id;
function ce(e) {
  process.env.NODE_ENV !== "production" && (e = e || /* @__PURE__ */ new Map());
  const t = process.env.NODE_ENV !== "production" ? (n) => ae(e, n) : ne;
  try {
    for (g = 0; g < d.length; g++) {
      const n = d[g];
      if (n && !(n.flags & 8)) {
        if (process.env.NODE_ENV !== "production" && t(n))
          continue;
        n.flags & 4 && (n.flags &= -2), X(
          n,
          n.i,
          n.i ? 15 : 14
        ), n.flags & 4 || (n.flags &= -2);
      }
    }
  } finally {
    for (; g < d.length; g++) {
      const n = d[g];
      n && (n.flags &= -2);
    }
    g = -1, d.length = 0, ve(e), q = null, (d.length || k.length) && ce(e);
  }
}
function ae(e, t) {
  const n = e.get(t) || 0;
  if (n > Pe) {
    const o = t.i, s = o && ye(o.type);
    return ie(
      `Maximum recursive updates exceeded${s ? ` in component <${s}>` : ""}. This means you have a reactive effect that is mutating its own dependencies and thus recursively triggering itself. Possible sources include component template, render function, updated hook or watcher source function.`,
      null,
      10
    ), !0;
  }
  return e.set(t, n + 1), !1;
}
const z = /* @__PURE__ */ new Map();
process.env.NODE_ENV !== "production" && (U().__VUE_HMR_RUNTIME__ = {
  createRecord: j(Le),
  rerender: j(ze),
  reload: j(je)
});
const I = /* @__PURE__ */ new Map();
function Le(e, t) {
  return I.has(e) ? !1 : (I.set(e, {
    initialDef: $(t),
    instances: /* @__PURE__ */ new Set()
  }), !0);
}
function $(e) {
  return Ne(e) ? e.__vccOpts : e;
}
function ze(e, t) {
  const n = I.get(e);
  n && (n.initialDef.render = t, [...n.instances].forEach((o) => {
    t && (o.render = t, $(o.type).render = t), o.renderCache = [], o.job.flags & 8 || o.update();
  }));
}
function je(e, t) {
  const n = I.get(e);
  if (!n) return;
  t = $(t), te(n.initialDef, t);
  const o = [...n.instances];
  for (let s = 0; s < o.length; s++) {
    const r = o[s], l = $(r.type);
    let c = z.get(l);
    c || (l !== n.initialDef && te(l, t), z.set(l, c = /* @__PURE__ */ new Set())), c.add(r), r.appContext.propsCache.delete(r.type), r.appContext.emitsCache.delete(r.type), r.appContext.optionsCache.delete(r.type), r.ceReload ? (c.add(r), r.ceReload(t.styles), c.delete(r)) : r.parent ? He(() => {
      r.job.flags & 8 || (r.parent.update(), c.delete(r));
    }) : r.appContext.reload ? r.appContext.reload() : typeof window < "u" ? window.location.reload() : console.warn(
      "[HMR] Root or manually mounted instance modified. Full reload required."
    ), r.root.ce && r !== r.root && r.root.ce._removeChildStyle(l);
  }
  Ue(() => {
    z.clear();
  });
}
function te(e, t) {
  H(e, t);
  for (const n in e)
    n !== "__file" && !(n in t) && delete e[n];
}
function j(e) {
  return (t, n) => {
    try {
      return e(t, n);
    } catch (o) {
      console.error(o), console.warn(
        "[HMR] Something went wrong during Vue component hot-reload. Full reload required."
      );
    }
  };
}
let C, x = [];
function ue(e, t) {
  var n, o;
  C = e, C ? (C.enabled = !0, x.forEach(({ event: s, args: r }) => C.emit(s, ...r)), x = []) : /* handle late devtools injection - only do this if we are in an actual */ /* browser environment to avoid the timer handle stalling test runner exit */ /* (#4815) */ typeof window < "u" && // some envs mock window but not fully
  window.HTMLElement && // also exclude jsdom
  // eslint-disable-next-line no-restricted-syntax
  !((o = (n = window.navigator) == null ? void 0 : n.userAgent) != null && o.includes("jsdom")) ? ((t.__VUE_DEVTOOLS_HOOK_REPLAY__ = t.__VUE_DEVTOOLS_HOOK_REPLAY__ || []).push((r) => {
    ue(r, t);
  }), setTimeout(() => {
    C || (t.__VUE_DEVTOOLS_HOOK_REPLAY__ = null, x = []);
  }, 3e3)) : x = [];
}
let A = null, Ke = null;
const Z = (e) => e.__isTeleport;
function Je(e) {
  let t = e[0];
  if (e.length > 1) {
    let n = !1;
    for (const o of e)
      if (o.type !== me) {
        if (process.env.NODE_ENV !== "production" && n) {
          w(
            "<transition> can only be used on a single element or component. Use <transition-group> for lists."
          );
          break;
        }
        if (t = o, n = !0, process.env.NODE_ENV === "production") break;
      }
  }
  return t;
}
function We(e) {
  if (!qe(e))
    return Z(e.type) && e.children ? Je(e.children) : e;
  if (e.component)
    return e.component.subTree;
  const { shapeFlag: t, children: n } = e;
  if (n) {
    if (t & 16)
      return n[0];
    if (t & 32 && b(n.default))
      return n.default();
  }
}
function fe(e, t) {
  if (e.shapeFlag & 6 && e.component) {
    e.transition = t;
    const n = e.component.subTree;
    fe(
      Z(n.type) && We(n) || n,
      t
    );
  } else e.shapeFlag & 128 ? (e.ssContent.transition = t.clone(e.ssContent), e.ssFallback.transition = t.clone(e.ssFallback)) : e.transition = t;
}
U().requestIdleCallback;
U().cancelIdleCallback;
const qe = (e) => e.type.__isKeepAlive, Be = /* @__PURE__ */ Symbol.for("v-ndc"), Ye = {};
process.env.NODE_ENV !== "production" && (Ye.ownKeys = (e) => (w(
  "Avoid app logic that relies on enumerating keys on a component instance. The keys will be empty in production mode to avoid performance overhead."
), Reflect.ownKeys(e)));
const Ge = {}, pe = (e) => Object.getPrototypeOf(e) === Ge, Qe = (e) => e.__isSuspense, de = /* @__PURE__ */ Symbol.for("v-fgt"), Xe = /* @__PURE__ */ Symbol.for("v-txt"), me = /* @__PURE__ */ Symbol.for("v-cmt");
function B(e) {
  return e ? e.__v_isVNode === !0 : !1;
}
const Ze = (...e) => _e(
  ...e
), he = ({ key: e }) => e ?? null, F = ({
  ref: e,
  ref_key: t,
  ref_for: n
}) => (typeof e == "number" && (e = "" + e), e != null ? y(e) || /* @__PURE__ */ Q(e) || b(e) ? { i: A, r: e, k: t, f: !!n } : e : null);
function et(e, t = null, n = null, o = 0, s = null, r = e === de ? 0 : 1, l = !1, c = !1) {
  const a = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: e,
    props: t,
    key: t && he(t),
    ref: t && F(t),
    scopeId: Ke,
    slotScopeIds: null,
    children: n,
    component: null,
    suspense: null,
    ssContent: null,
    ssFallback: null,
    dirs: null,
    transition: null,
    el: null,
    anchor: null,
    target: null,
    targetStart: null,
    targetAnchor: null,
    staticCount: 0,
    shapeFlag: r,
    patchFlag: o,
    dynamicProps: s,
    dynamicChildren: null,
    appContext: null,
    ctx: A
  };
  if (c ? (M(a, n), r & 128 && e.normalize(a)) : n && (a.shapeFlag |= y(n) ? 8 : 16), process.env.NODE_ENV !== "production" && a.key !== a.key && w("VNode created with invalid key (NaN). VNode type:", a.type), process.env.NODE_ENV !== "production" && t && a.shapeFlag & 1) {
    const f = t.innerHTML != null ? "innerHTML" : t.textContent != null ? "textContent" : null;
    f && tt(a.children) && w(
      `The \`${f}\` prop on <${a.type}> will override its children. Remove either the \`${f}\` prop or the children.`
    );
  }
  return a;
}
function tt(e) {
  return y(e) ? e !== "" : m(e) ? e.length > 0 : !1;
}
const T = process.env.NODE_ENV !== "production" ? Ze : _e;
function _e(e, t = null, n = null, o = 0, s = null, r = !1) {
  if ((!e || e === Be) && (process.env.NODE_ENV !== "production" && !e && w(`Invalid vnode type when creating vnode: ${e}.`), e = me), B(e)) {
    const c = P(
      e,
      t,
      !0
      /* mergeRef: true */
    );
    return n && M(c, n), c.patchFlag = -2, c;
  }
  if (Ne(e) && (e = e.__vccOpts), t) {
    t = nt(t);
    let { class: c, style: a } = t;
    c && !y(c) && (t.class = G(c)), N(a) && (/* @__PURE__ */ W(a) && !m(a) && (a = H({}, a)), t.style = Y(a));
  }
  const l = y(e) ? 1 : Qe(e) ? 128 : Z(e) ? 64 : N(e) ? 4 : b(e) ? 2 : 0;
  return process.env.NODE_ENV !== "production" && l & 4 && /* @__PURE__ */ W(e) && (e = /* @__PURE__ */ O(e), w(
    "Vue received a Component that was made a reactive object. This can lead to unnecessary performance overhead and should be avoided by marking the component with `markRaw` or using `shallowRef` instead of `ref`.",
    `
Component that was made reactive: `,
    e
  )), et(
    e,
    t,
    n,
    o,
    s,
    l,
    r,
    !0
  );
}
function nt(e) {
  return e ? /* @__PURE__ */ W(e) || pe(e) ? H({}, e) : e : null;
}
function P(e, t, n = !1, o = !1) {
  const { props: s, ref: r, patchFlag: l, children: c, transition: a } = e, f = t ? rt(s || {}, t) : s, _ = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: e.type,
    props: f,
    key: f && he(f),
    ref: t && t.ref ? (
      // #2078 in the case of <component :is="vnode" ref="extra"/>
      // if the vnode itself already has a ref, cloneVNode will need to merge
      // the refs so the single vnode can be set on multiple refs
      n && r ? m(r) ? r.concat(F(t)) : [r, F(t)] : F(t)
    ) : r,
    scopeId: e.scopeId,
    slotScopeIds: e.slotScopeIds,
    children: process.env.NODE_ENV !== "production" && l === -1 && m(c) ? c.map(ge) : c,
    target: e.target,
    targetStart: e.targetStart,
    targetAnchor: e.targetAnchor,
    staticCount: e.staticCount,
    shapeFlag: e.shapeFlag,
    // if the vnode is cloned with extra props, we can no longer assume its
    // existing patch flag to be reliable and need to add the FULL_PROPS flag.
    // note: preserve flag for fragments since they use the flag for children
    // fast paths only.
    patchFlag: t && e.type !== de ? l === -1 ? 16 : l | 16 : l,
    dynamicProps: e.dynamicProps,
    dynamicChildren: e.dynamicChildren,
    appContext: e.appContext,
    dirs: e.dirs,
    transition: a,
    // These should technically only be non-null on mounted VNodes. However,
    // they *should* be copied for kept-alive vnodes. So we just always copy
    // them since them being non-null during a mount doesn't affect the logic as
    // they will simply be overwritten.
    component: e.component,
    suspense: e.suspense,
    ssContent: e.ssContent && P(e.ssContent),
    ssFallback: e.ssFallback && P(e.ssFallback),
    placeholder: e.placeholder,
    el: e.el,
    anchor: e.anchor,
    ctx: e.ctx,
    ce: e.ce
  };
  return a && o && fe(
    _,
    a.clone(_)
  ), _;
}
function ge(e) {
  const t = P(e);
  return m(e.children) && (t.children = e.children.map(ge)), t;
}
function ot(e = " ", t = 0) {
  return T(Xe, null, e, t);
}
function M(e, t) {
  let n = 0;
  const { shapeFlag: o } = e;
  if (t == null)
    t = null;
  else if (m(t))
    n = 16;
  else if (typeof t == "object")
    if (o & 65) {
      const s = t.default;
      s && (s._c && (s._d = !1), M(e, s()), s._c && (s._d = !0));
      return;
    } else
      n = 32, !t._ && !pe(t) && (t._ctx = A);
  else if (b(t)) {
    if (o & 65) {
      M(e, { default: t });
      return;
    }
    t = { default: t, _ctx: A }, n = 32;
  } else
    t = String(t), o & 64 ? (n = 16, t = [ot(t)]) : n = 8;
  e.children = t, e.shapeFlag |= n;
}
function rt(...e) {
  const t = {};
  for (let n = 0; n < e.length; n++) {
    const o = e[n];
    for (const s in o)
      if (s === "class")
        t.class !== o.class && (t.class = G([t.class, o.class]));
      else if (s === "style")
        t.style = Y([t.style, o.style]);
      else if (we(s)) {
        const r = t[s], l = o[s];
        l && r !== l && !(m(r) && r.includes(l)) ? t[s] = r ? [].concat(r, l) : l : l == null && r == null && // mergeProps({ 'onUpdate:modelValue': undefined }) should not retain
        // the model listener.
        !be(s) && (t[s] = l);
      } else s !== "" && (t[s] = o[s]);
  }
  return t;
}
{
  const e = U(), t = (n, o) => {
    let s;
    return (s = e[n]) || (s = e[n] = []), s.push(o), (r) => {
      s.length > 1 ? s.forEach((l) => l(r)) : s[0](r);
    };
  };
  t(
    "__VUE_INSTANCE_SETTERS__",
    (n) => n
  ), t(
    "__VUE_SSR_SETTERS__",
    (n) => n
  );
}
process.env.NODE_ENV;
const st = /(?:^|[-_])\w/g, it = (e) => e.replace(st, (t) => t.toUpperCase()).replace(/[-_]/g, "");
function ye(e, t = !0) {
  return b(e) ? e.displayName || e.name : e.name || t && e.__name;
}
function Ee(e, t, n = !1) {
  let o = ye(t);
  if (!o && t.__file) {
    const s = t.__file.match(/([^/\\]+)\.\w+$/);
    s && (o = s[1]);
  }
  if (!o && e) {
    const s = (r) => {
      for (const l in r)
        if (r[l] === t)
          return l;
    };
    o = s(e.components) || e.parent && s(
      e.parent.type.components
    ) || s(e.appContext.components);
  }
  return o ? it(o) : n ? "App" : "Anonymous";
}
function Ne(e) {
  return b(e) && "__vccOpts" in e;
}
function at(e, t, n) {
  try {
    const o = arguments.length;
    return o === 2 ? N(t) && !m(t) ? B(t) ? T(e, null, [t]) : T(e, t) : T(e, null, t) : (o > 3 ? n = Array.prototype.slice.call(arguments, 2) : o === 3 && B(n) && (n = [n]), T(e, t, n));
  } finally {
  }
}
function lt() {
  if (process.env.NODE_ENV === "production" || typeof window > "u")
    return;
  const e = { style: "color:#3ba776" }, t = { style: "color:#1677ff" }, n = { style: "color:#f5222d" }, o = { style: "color:#eb2f96" }, s = {
    __vue_custom_formatter: !0,
    header(i) {
      if (!N(i))
        return null;
      if (i.__isVue)
        return ["div", e, "VueInstance"];
      if (/* @__PURE__ */ Q(i)) {
        const u = i.value;
        return [
          "div",
          {},
          ["span", e, _(i)],
          "<",
          c(u),
          ">"
        ];
      } else {
        if (/* @__PURE__ */ oe(i))
          return [
            "div",
            {},
            ["span", e, /* @__PURE__ */ v(i) ? "ShallowReactive" : "Reactive"],
            "<",
            c(i),
            `>${/* @__PURE__ */ J(i) ? " (readonly)" : ""}`
          ];
        if (/* @__PURE__ */ J(i))
          return [
            "div",
            {},
            ["span", e, /* @__PURE__ */ v(i) ? "ShallowReadonly" : "Readonly"],
            "<",
            c(i),
            ">"
          ];
      }
      return null;
    },
    hasBody(i) {
      return i && i.__isVue;
    },
    body(i) {
      if (i && i.__isVue)
        return [
          "div",
          {},
          ...r(i.$)
        ];
    }
  };
  function r(i) {
    const u = [];
    i.type.props && i.props && u.push(l("props", /* @__PURE__ */ O(i.props))), i.setupState !== K && u.push(l("setup", i.setupState)), i.data !== K && u.push(l("data", /* @__PURE__ */ O(i.data)));
    const p = a(i, "computed");
    p && u.push(l("computed", p));
    const h = a(i, "inject");
    return h && u.push(l("injected", h)), u.push([
      "div",
      {},
      [
        "span",
        {
          style: o.style + ";opacity:0.66"
        },
        "$ (internal): "
      ],
      ["object", { object: i }]
    ]), u;
  }
  function l(i, u) {
    return u = H({}, u), Object.keys(u).length ? [
      "div",
      { style: "line-height:1.25em;margin-bottom:0.6em" },
      [
        "div",
        {
          style: "color:#476582"
        },
        i
      ],
      [
        "div",
        {
          style: "padding-left:1.25em"
        },
        ...Object.keys(u).map((p) => [
          "div",
          {},
          ["span", o, p + ": "],
          c(u[p], !1)
        ])
      ]
    ] : ["span", {}];
  }
  function c(i, u = !0) {
    return typeof i == "number" ? ["span", t, i] : typeof i == "string" ? ["span", n, JSON.stringify(i)] : typeof i == "boolean" ? ["span", o, i] : N(i) ? ["object", { object: u ? /* @__PURE__ */ O(i) : i }] : ["span", n, String(i)];
  }
  function a(i, u) {
    const p = i.type;
    if (b(p))
      return;
    const h = {};
    for (const R in i.ctx)
      f(p, R, u) && (h[R] = i.ctx[R]);
    return h;
  }
  function f(i, u, p) {
    const h = i[p];
    if (m(h) && h.includes(u) || N(h) && u in h || i.extends && f(i.extends, u, p) || i.mixins && i.mixins.some((R) => f(R, u, p)))
      return !0;
  }
  function _(i) {
    return /* @__PURE__ */ v(i) ? "ShallowRef" : i.effect ? "ComputedRef" : "Ref";
  }
  window.devtoolsFormatters ? window.devtoolsFormatters.push(s) : window.devtoolsFormatters = [s];
}
process.env.NODE_ENV;
process.env.NODE_ENV;
process.env.NODE_ENV;
/**
* vue v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
function ct() {
  lt();
}
process.env.NODE_ENV !== "production" && ct();
export {
  at as h
};
