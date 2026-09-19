/**
* @vue/shared v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
const O = (t) => t.charCodeAt(0) === 111 && t.charCodeAt(1) === 110 && // uppercase letter
(t.charCodeAt(2) > 122 || t.charCodeAt(2) < 97), w = (t) => t.startsWith("onUpdate:"), E = Object.assign, f = Array.isArray, m = (t) => typeof t == "function", a = (t) => typeof t == "string", M = (t) => typeof t == "symbol", g = (t) => t !== null && typeof t == "object";
let I;
const T = () => I || (I = typeof globalThis < "u" ? globalThis : typeof self < "u" ? self : typeof window < "u" ? window : typeof global < "u" ? global : {});
function N(t) {
  if (f(t)) {
    const e = {};
    for (let n = 0; n < t.length; n++) {
      const l = t[n], s = a(l) ? K(l) : N(l);
      if (s)
        for (const i in s)
          e[i] = s[i];
    }
    return e;
  } else if (a(t) || g(t))
    return t;
}
const U = /;(?![^(]*\))/g, B = /:([^]+)/, D = /\/\*[^]*?\*\//g;
function K(t) {
  const e = {};
  return t.replace(D, "").split(U).forEach((n) => {
    if (n) {
      const l = n.split(B);
      l.length > 1 && (e[l[0].trim()] = l[1].trim());
    }
  }), e;
}
function h(t) {
  let e = "";
  if (a(t))
    e = t;
  else if (f(t))
    for (let n = 0; n < t.length; n++) {
      const l = h(t[n]);
      l && (e += l + " ");
    }
  else if (g(t))
    for (const n in t)
      t[n] && (e += n + " ");
  return e.trim();
}
/**
* @vue/reactivity v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
new Set(
  /* @__PURE__ */ Object.getOwnPropertyNames(Symbol).filter((t) => t !== "arguments" && t !== "caller").map((t) => Symbol[t]).filter(M)
);
// @__NO_SIDE_EFFECTS__
function V(t) {
  return t ? !!t.__v_raw : !1;
}
// @__NO_SIDE_EFFECTS__
function L(t) {
  return t ? t.__v_isRef === !0 : !1;
}
/**
* @vue/runtime-core v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
let b = null, P = null;
const A = (t) => t.__isTeleport;
function G(t) {
  let e = t[0];
  if (t.length > 1) {
    for (const n of t)
      if (n.type !== x) {
        e = n;
        break;
      }
  }
  return e;
}
function q(t) {
  if (!H(t))
    return A(t.type) && t.children ? G(t.children) : t;
  if (t.component)
    return t.component.subTree;
  const { shapeFlag: e, children: n } = t;
  if (n) {
    if (e & 16)
      return n[0];
    if (e & 32 && m(n.default))
      return n.default();
  }
}
function k(t, e) {
  if (t.shapeFlag & 6 && t.component) {
    t.transition = e;
    const n = t.component.subTree;
    k(
      A(n.type) && q(n) || n,
      e
    );
  } else t.shapeFlag & 128 ? (t.ssContent.transition = e.clone(t.ssContent), t.ssFallback.transition = e.clone(t.ssFallback)) : t.transition = e;
}
T().requestIdleCallback;
T().cancelIdleCallback;
const H = (t) => t.type.__isKeepAlive, W = /* @__PURE__ */ Symbol.for("v-ndc"), Y = {}, R = (t) => Object.getPrototypeOf(t) === Y, $ = (t) => t.__isSuspense, j = /* @__PURE__ */ Symbol.for("v-fgt"), d = /* @__PURE__ */ Symbol.for("v-txt"), x = /* @__PURE__ */ Symbol.for("v-cmt");
function p(t) {
  return t ? t.__v_isVNode === !0 : !1;
}
const z = ({ key: t }) => t ?? null, _ = ({
  ref: t,
  ref_key: e,
  ref_for: n
}) => (typeof t == "number" && (t = "" + t), t != null ? a(t) || /* @__PURE__ */ L(t) || m(t) ? { i: b, r: t, k: e, f: !!n } : t : null);
function J(t, e = null, n = null, l = 0, s = null, i = t === j ? 0 : 1, c = !1, r = !1) {
  const o = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: t,
    props: e,
    key: e && z(e),
    ref: e && _(e),
    scopeId: P,
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
    shapeFlag: i,
    patchFlag: l,
    dynamicProps: s,
    dynamicChildren: null,
    appContext: null,
    ctx: b
  };
  return r ? (y(o, n), i & 128 && t.normalize(o)) : n && (o.shapeFlag |= a(n) ? 8 : 16), o;
}
const u = Q;
function Q(t, e = null, n = null, l = 0, s = null, i = !1) {
  if ((!t || t === W) && (t = x), p(t)) {
    const r = F(
      t,
      e,
      !0
      /* mergeRef: true */
    );
    return n && y(r, n), r.patchFlag = -2, r;
  }
  if (tt(t) && (t = t.__vccOpts), e) {
    e = X(e);
    let { class: r, style: o } = e;
    r && !a(r) && (e.class = h(r)), g(o) && (/* @__PURE__ */ V(o) && !f(o) && (o = E({}, o)), e.style = N(o));
  }
  const c = a(t) ? 1 : $(t) ? 128 : A(t) ? 64 : g(t) ? 4 : m(t) ? 2 : 0;
  return J(
    t,
    e,
    n,
    l,
    s,
    c,
    i,
    !0
  );
}
function X(t) {
  return t ? /* @__PURE__ */ V(t) || R(t) ? E({}, t) : t : null;
}
function F(t, e, n = !1, l = !1) {
  const { props: s, ref: i, patchFlag: c, children: r, transition: o } = t, S = e ? v(s || {}, e) : s, C = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: t.type,
    props: S,
    key: S && z(S),
    ref: e && e.ref ? (
      // #2078 in the case of <component :is="vnode" ref="extra"/>
      // if the vnode itself already has a ref, cloneVNode will need to merge
      // the refs so the single vnode can be set on multiple refs
      n && i ? f(i) ? i.concat(_(e)) : [i, _(e)] : _(e)
    ) : i,
    scopeId: t.scopeId,
    slotScopeIds: t.slotScopeIds,
    children: r,
    target: t.target,
    targetStart: t.targetStart,
    targetAnchor: t.targetAnchor,
    staticCount: t.staticCount,
    shapeFlag: t.shapeFlag,
    // if the vnode is cloned with extra props, we can no longer assume its
    // existing patch flag to be reliable and need to add the FULL_PROPS flag.
    // note: preserve flag for fragments since they use the flag for children
    // fast paths only.
    patchFlag: e && t.type !== j ? c === -1 ? 16 : c | 16 : c,
    dynamicProps: t.dynamicProps,
    dynamicChildren: t.dynamicChildren,
    appContext: t.appContext,
    dirs: t.dirs,
    transition: o,
    // These should technically only be non-null on mounted VNodes. However,
    // they *should* be copied for kept-alive vnodes. So we just always copy
    // them since them being non-null during a mount doesn't affect the logic as
    // they will simply be overwritten.
    component: t.component,
    suspense: t.suspense,
    ssContent: t.ssContent && F(t.ssContent),
    ssFallback: t.ssFallback && F(t.ssFallback),
    placeholder: t.placeholder,
    el: t.el,
    anchor: t.anchor,
    ctx: t.ctx,
    ce: t.ce
  };
  return o && l && k(
    C,
    o.clone(C)
  ), C;
}
function Z(t = " ", e = 0) {
  return u(d, null, t, e);
}
function y(t, e) {
  let n = 0;
  const { shapeFlag: l } = t;
  if (e == null)
    e = null;
  else if (f(e))
    n = 16;
  else if (typeof e == "object")
    if (l & 65) {
      const s = e.default;
      s && (s._c && (s._d = !1), y(t, s()), s._c && (s._d = !0));
      return;
    } else
      n = 32, !e._ && !R(e) && (e._ctx = b);
  else if (m(e)) {
    if (l & 65) {
      y(t, { default: e });
      return;
    }
    e = { default: e, _ctx: b }, n = 32;
  } else
    e = String(e), l & 64 ? (n = 16, e = [Z(e)]) : n = 8;
  t.children = e, t.shapeFlag |= n;
}
function v(...t) {
  const e = {};
  for (let n = 0; n < t.length; n++) {
    const l = t[n];
    for (const s in l)
      if (s === "class")
        e.class !== l.class && (e.class = h([e.class, l.class]));
      else if (s === "style")
        e.style = N([e.style, l.style]);
      else if (O(s)) {
        const i = e[s], c = l[s];
        c && i !== c && !(f(i) && i.includes(c)) ? e[s] = i ? [].concat(i, c) : c : c == null && i == null && // mergeProps({ 'onUpdate:modelValue': undefined }) should not retain
        // the model listener.
        !w(s) && (e[s] = c);
      } else s !== "" && (e[s] = l[s]);
  }
  return e;
}
{
  const t = T(), e = (n, l) => {
    let s;
    return (s = t[n]) || (s = t[n] = []), s.push(l), (i) => {
      s.length > 1 ? s.forEach((c) => c(i)) : s[0](i);
    };
  };
  e(
    "__VUE_INSTANCE_SETTERS__",
    (n) => n
  ), e(
    "__VUE_SSR_SETTERS__",
    (n) => n
  );
}
function tt(t) {
  return m(t) && "__vccOpts" in t;
}
function et(t, e, n) {
  try {
    const l = arguments.length;
    return l === 2 ? g(e) && !f(e) ? p(e) ? u(t, null, [e]) : u(t, e) : u(t, null, e) : (l > 3 ? n = Array.prototype.slice.call(arguments, 2) : l === 3 && p(n) && (n = [n]), u(t, e, n));
  } finally {
  }
}
export {
  et as h
};
