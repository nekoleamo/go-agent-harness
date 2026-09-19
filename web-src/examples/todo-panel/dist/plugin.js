/**
* @vue/shared v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
// @__NO_SIDE_EFFECTS__
function Bt(t) {
  const e = /* @__PURE__ */ Object.create(null);
  for (const n of t.split(",")) e[n] = 1;
  return (n) => n in e;
}
const Ut = {}, Yt = (t) => t.charCodeAt(0) === 111 && t.charCodeAt(1) === 110 && // uppercase letter
(t.charCodeAt(2) > 122 || t.charCodeAt(2) < 97), $t = (t) => t.startsWith("onUpdate:"), $ = Object.assign, Gt = Object.prototype.hasOwnProperty, X = (t, e) => Gt.call(t, e), g = Array.isArray, z = (t) => St(t) === "[object Map]", S = (t) => typeof t == "function", I = (t) => typeof t == "string", V = (t) => typeof t == "symbol", m = (t) => t !== null && typeof t == "object", qt = (t) => (m(t) || S(t)) && S(t.then) && S(t.catch), Jt = Object.prototype.toString, St = (t) => Jt.call(t), Qt = (t) => St(t).slice(8, -1), it = (t) => I(t) && t !== "NaN" && t[0] !== "-" && "" + parseInt(t, 10) === t, y = (t, e) => !Object.is(t, e);
let mt;
const ot = () => mt || (mt = typeof globalThis < "u" ? globalThis : typeof self < "u" ? self : typeof window < "u" ? window : typeof global < "u" ? global : {});
function ct(t) {
  if (g(t)) {
    const e = {};
    for (let n = 0; n < t.length; n++) {
      const r = t[n], s = I(r) ? te(r) : ct(r);
      if (s)
        for (const i in s)
          e[i] = s[i];
    }
    return e;
  } else if (I(t) || m(t))
    return t;
}
const Xt = /;(?![^(]*\))/g, Zt = /:([^]+)/, kt = /\/\*[^]*?\*\//g;
function te(t) {
  const e = {};
  return t.replace(kt, "").split(Xt).forEach((n) => {
    if (n) {
      const r = n.split(Zt);
      r.length > 1 && (e[r[0].trim()] = r[1].trim());
    }
  }), e;
}
function lt(t) {
  let e = "";
  if (I(t))
    e = t;
  else if (g(t))
    for (let n = 0; n < t.length; n++) {
      const r = lt(t[n]);
      r && (e += r + " ");
    }
  else if (m(t))
    for (const n in t)
      t[n] && (e += n + " ");
  return e.trim();
}
/**
* @vue/reactivity v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
let d, xt = 0, P, F;
function ee(t, e = !1) {
  if (t.flags |= 8, e) {
    t.next = F, F = t;
    return;
  }
  t.next = P, P = t;
}
function at() {
  xt++;
}
function ut() {
  if (--xt > 0)
    return;
  if (F) {
    let e = F;
    for (F = void 0; e; ) {
      const n = e.next;
      e.next = void 0, e.flags &= -9, e = n;
    }
  }
  let t;
  for (; P; ) {
    let e = P;
    for (P = void 0; e; ) {
      const n = e.next;
      if (e.next = void 0, e.flags &= -9, e.flags & 1)
        try {
          e.trigger();
        } catch (r) {
          t || (t = r);
        }
      e = n;
    }
  }
  if (t) throw t;
}
function ne(t) {
  for (let e = t.deps; e; e = e.nextDep)
    e.version = -1, e.prevActiveLink = e.dep.activeLink, e.dep.activeLink = e;
}
function se(t) {
  let e, n = t.depsTail, r = n;
  for (; r; ) {
    const s = r.prevDep;
    r.version === -1 ? (r === n && (n = s), yt(r), ie(r)) : e = r, r.dep.activeLink = r.prevActiveLink, r.prevActiveLink = void 0, r = s;
  }
  t.deps = e, t.depsTail = n;
}
function re(t) {
  for (let e = t.deps; e; e = e.nextDep)
    if (e.dep.version !== e.version || e.dep.computed && (wt(e.dep.computed) || e.dep.version !== e.version))
      return !0;
  return !!t._dirty;
}
function wt(t) {
  if (t.flags & 4 && !(t.flags & 16) || (t.flags &= -17, t.globalVersion === N) || (t.globalVersion = N, !t.isSSR && t.flags & 128 && (!t.deps && !t._dirty || !re(t))))
    return;
  t.flags |= 2;
  const e = t.dep, n = d, r = T;
  d = t, T = !0;
  try {
    ne(t);
    const s = t.fn(t._value);
    (e.version === 0 || y(s, t._value)) && (t.flags |= 128, t._value = s, e.version++);
  } catch (s) {
    throw e.version++, s;
  } finally {
    d = n, T = r, se(t), t.flags &= -3;
  }
}
function yt(t, e = !1) {
  const { dep: n, prevSub: r, nextSub: s } = t;
  if (r && (r.nextSub = s, t.prevSub = void 0), s && (s.prevSub = r, t.nextSub = void 0), n.subs === t && (n.subs = r, !r && n.computed)) {
    n.computed.flags &= -5;
    for (let i = n.computed.deps; i; i = i.nextDep)
      yt(i, !0);
  }
  !e && !--n.sc && n.map && n.map.delete(n.key);
}
function ie(t) {
  const { prevDep: e, nextDep: n } = t;
  e && (e.nextDep = n, t.prevDep = void 0), n && (n.prevDep = e, t.nextDep = void 0);
}
let T = !0;
const Rt = [];
function ft() {
  Rt.push(T), T = !1;
}
function pt() {
  const t = Rt.pop();
  T = t === void 0 ? !0 : t;
}
let N = 0;
class oe {
  constructor(e, n) {
    this.sub = e, this.dep = n, this.version = n.version, this.nextDep = this.prevDep = this.nextSub = this.prevSub = this.prevActiveLink = void 0;
  }
}
class dt {
  // TODO isolatedDeclarations "__v_skip"
  constructor(e) {
    this.computed = e, this.version = 0, this.activeLink = void 0, this.subs = void 0, this.map = void 0, this.key = void 0, this.sc = 0, this.__v_skip = !0;
  }
  track(e) {
    if (!d || !T || d === this.computed)
      return;
    let n = this.activeLink;
    if (n === void 0 || n.sub !== d)
      n = this.activeLink = new oe(d, this), d.deps ? (n.prevDep = d.depsTail, d.depsTail.nextDep = n, d.depsTail = n) : d.deps = d.depsTail = n, Tt(n);
    else if (n.version === -1 && (n.version = this.version, n.nextDep)) {
      const r = n.nextDep;
      r.prevDep = n.prevDep, n.prevDep && (n.prevDep.nextDep = r), n.prevDep = d.depsTail, n.nextDep = void 0, d.depsTail.nextDep = n, d.depsTail = n, d.deps === n && (d.deps = r);
    }
    return n;
  }
  trigger(e) {
    this.version++, N++, this.notify(e);
  }
  notify(e) {
    at();
    try {
      for (let n = this.subs; n; n = n.prevSub)
        n.sub.notify() && n.sub.dep.notify();
    } finally {
      ut();
    }
  }
}
function Tt(t) {
  if (t.dep.sc++, t.sub.flags & 4) {
    const e = t.dep.computed;
    if (e && !t.dep.subs) {
      e.flags |= 20;
      for (let r = e.deps; r; r = r.nextDep)
        Tt(r);
    }
    const n = t.dep.subs;
    n !== t && (t.prevSub = n, n && (n.nextSub = t)), t.dep.subs = t;
  }
}
const Z = /* @__PURE__ */ new WeakMap(), E = /* @__PURE__ */ Symbol(
  ""
), k = /* @__PURE__ */ Symbol(
  ""
), L = /* @__PURE__ */ Symbol(
  ""
);
function _(t, e, n) {
  if (T && d) {
    let r = Z.get(t);
    r || Z.set(t, r = /* @__PURE__ */ new Map());
    let s = r.get(n);
    s || (r.set(n, s = new dt()), s.map = r, s.key = n), s.track();
  }
}
function R(t, e, n, r, s, i) {
  const o = Z.get(t);
  if (!o) {
    N++;
    return;
  }
  const c = (l) => {
    l && l.trigger();
  };
  if (at(), e === "clear")
    o.forEach(c);
  else {
    const l = g(t), a = l && it(n);
    if (l && n === "length") {
      const f = Number(r);
      o.forEach((p, b) => {
        (b === "length" || b === L || !V(b) && b >= f) && c(p);
      });
    } else
      switch ((n !== void 0 || o.has(void 0)) && c(o.get(n)), a && c(o.get(L)), e) {
        case "add":
          l ? a && c(o.get("length")) : (c(o.get(E)), z(t) && c(o.get(k)));
          break;
        case "delete":
          l || (c(o.get(E)), z(t) && c(o.get(k)));
          break;
        case "set":
          z(t) && c(o.get(E));
          break;
      }
  }
  ut();
}
function O(t) {
  const e = /* @__PURE__ */ u(t);
  return e === t ? e : (_(e, "iterate", L), /* @__PURE__ */ A(t) ? e : e.map(w));
}
function ht(t) {
  return _(t = /* @__PURE__ */ u(t), "iterate", L), t;
}
function v(t, e) {
  return /* @__PURE__ */ C(t) ? K(/* @__PURE__ */ jt(t) ? w(e) : e) : w(e);
}
const ce = {
  __proto__: null,
  [Symbol.iterator]() {
    return q(this, Symbol.iterator, (t) => v(this, t));
  },
  concat(...t) {
    return O(this).concat(
      ...t.map((e) => g(e) ? O(e) : e)
    );
  },
  entries() {
    return q(this, "entries", (t) => (t[1] = v(this, t[1]), t));
  },
  every(t, e) {
    return x(this, "every", t, e, void 0, arguments);
  },
  filter(t, e) {
    return x(
      this,
      "filter",
      t,
      e,
      (n) => n.map((r) => v(this, r)),
      arguments
    );
  },
  find(t, e) {
    return x(
      this,
      "find",
      t,
      e,
      (n) => v(this, n),
      arguments
    );
  },
  findIndex(t, e) {
    return x(this, "findIndex", t, e, void 0, arguments);
  },
  findLast(t, e) {
    return x(
      this,
      "findLast",
      t,
      e,
      (n) => v(this, n),
      arguments
    );
  },
  findLastIndex(t, e) {
    return x(this, "findLastIndex", t, e, void 0, arguments);
  },
  // flat, flatMap could benefit from ARRAY_ITERATE but are not straight-forward to implement
  forEach(t, e) {
    return x(this, "forEach", t, e, void 0, arguments);
  },
  includes(...t) {
    return J(this, "includes", t);
  },
  indexOf(...t) {
    return J(this, "indexOf", t);
  },
  join(t) {
    return O(this).join(t);
  },
  // keys() iterator only reads `length`, no optimization required
  lastIndexOf(...t) {
    return J(this, "lastIndexOf", t);
  },
  map(t, e) {
    return x(this, "map", t, e, void 0, arguments);
  },
  pop() {
    return M(this, "pop");
  },
  push(...t) {
    return M(this, "push", t);
  },
  reduce(t, ...e) {
    return vt(this, "reduce", t, e);
  },
  reduceRight(t, ...e) {
    return vt(this, "reduceRight", t, e);
  },
  shift() {
    return M(this, "shift");
  },
  // slice could use ARRAY_ITERATE but also seems to beg for range tracking
  some(t, e) {
    return x(this, "some", t, e, void 0, arguments);
  },
  splice(...t) {
    return M(this, "splice", t);
  },
  toReversed() {
    return O(this).toReversed();
  },
  toSorted(t) {
    return O(this).toSorted(t);
  },
  toSpliced(...t) {
    return O(this).toSpliced(...t);
  },
  unshift(...t) {
    return M(this, "unshift", t);
  },
  values() {
    return q(this, "values", (t) => v(this, t));
  }
};
function q(t, e, n) {
  const r = ht(t), s = r[e]();
  return r !== t && !/* @__PURE__ */ A(t) && (s._next = s.next, s.next = () => {
    const i = s._next();
    return i.done || (i.value = n(i.value)), i;
  }), s;
}
const le = Array.prototype;
function x(t, e, n, r, s, i) {
  const o = ht(t), c = o !== t && !/* @__PURE__ */ A(t), l = o[e];
  if (l !== le[e]) {
    const p = l.apply(t, i);
    return c ? w(p) : p;
  }
  let a = n;
  o !== t && (c ? a = function(p, b) {
    return n.call(this, v(t, p), b, t);
  } : n.length > 2 && (a = function(p, b) {
    return n.call(this, p, b, t);
  }));
  const f = l.call(o, a, r);
  return c && s ? s(f) : f;
}
function vt(t, e, n, r) {
  const s = ht(t), i = s !== t && !/* @__PURE__ */ A(t);
  let o = n, c = !1;
  s !== t && (i ? (c = r.length === 0, o = function(a, f, p) {
    return c && (c = !1, a = v(t, a)), n.call(this, a, v(t, f), p, t);
  }) : n.length > 3 && (o = function(a, f, p) {
    return n.call(this, a, f, p, t);
  }));
  const l = s[e](o, ...r);
  return c ? v(t, l) : l;
}
function J(t, e, n) {
  const r = /* @__PURE__ */ u(t);
  _(r, "iterate", L);
  const s = r[e](...n);
  return (s === -1 || s === !1) && /* @__PURE__ */ _t(n[0]) ? (n[0] = /* @__PURE__ */ u(n[0]), r[e](...n)) : s;
}
function M(t, e, n = []) {
  ft(), at();
  const r = (/* @__PURE__ */ u(t))[e].apply(t, n);
  return ut(), pt(), r;
}
const ae = /* @__PURE__ */ Bt("__proto__,__v_isRef,__isVue"), It = new Set(
  /* @__PURE__ */ Object.getOwnPropertyNames(Symbol).filter((t) => t !== "arguments" && t !== "caller").map((t) => Symbol[t]).filter(V)
);
function ue(t) {
  V(t) || (t = String(t));
  const e = /* @__PURE__ */ u(this);
  return _(e, "has", t), e.hasOwnProperty(t);
}
class Ct {
  constructor(e = !1, n = !1) {
    this._isReadonly = e, this._isShallow = n;
  }
  get(e, n, r) {
    if (n === "__v_skip") return e.__v_skip;
    const s = this._isReadonly, i = this._isShallow;
    if (n === "__v_isReactive")
      return !s;
    if (n === "__v_isReadonly")
      return s;
    if (n === "__v_isShallow")
      return i;
    if (n === "__v_raw")
      return r === (s ? i ? Se : Dt : i ? ve : Et).get(e) || // receiver is not the reactive proxy, but has the same prototype
      // this means the receiver is a user proxy of the reactive proxy
      Object.getPrototypeOf(e) === Object.getPrototypeOf(r) ? e : void 0;
    const o = g(e);
    if (!s) {
      let l;
      if (o && (l = ce[n]))
        return l;
      if (n === "hasOwnProperty")
        return ue;
    }
    const c = Reflect.get(
      e,
      n,
      // if this is a proxy wrapping a ref, return methods using the raw ref
      // as receiver so that we don't have to call `toRaw` on the ref in all
      // its class methods
      /* @__PURE__ */ D(e) ? e : r
    );
    if ((V(n) ? It.has(n) : ae(n)) || (s || _(e, "get", n), i))
      return c;
    if (/* @__PURE__ */ D(c)) {
      const l = o && it(n) ? c : c.value;
      return s && m(l) ? /* @__PURE__ */ et(l) : l;
    }
    return m(c) ? s ? /* @__PURE__ */ et(c) : /* @__PURE__ */ Ot(c) : c;
  }
}
class fe extends Ct {
  constructor(e = !1) {
    super(!1, e);
  }
  set(e, n, r, s) {
    let i = e[n];
    const o = g(e) && it(n);
    if (!this._isShallow) {
      const a = /* @__PURE__ */ C(i);
      if (!/* @__PURE__ */ A(r) && !/* @__PURE__ */ C(r) && (i = /* @__PURE__ */ u(i), r = /* @__PURE__ */ u(r)), !o && /* @__PURE__ */ D(i) && !/* @__PURE__ */ D(r))
        return a || (i.value = r), !0;
    }
    const c = o ? Number(n) < e.length : X(e, n), l = Reflect.set(
      e,
      n,
      r,
      /* @__PURE__ */ D(e) ? e : s
    );
    return e === /* @__PURE__ */ u(s) && l && (c ? y(r, i) && R(e, "set", n, r) : R(e, "add", n, r)), l;
  }
  deleteProperty(e, n) {
    const r = X(e, n);
    e[n];
    const s = Reflect.deleteProperty(e, n);
    return s && r && R(e, "delete", n, void 0), s;
  }
  has(e, n) {
    const r = Reflect.has(e, n);
    return (!V(n) || !It.has(n)) && _(e, "has", n), r;
  }
  ownKeys(e) {
    return _(
      e,
      "iterate",
      g(e) ? "length" : E
    ), Reflect.ownKeys(e);
  }
}
class pe extends Ct {
  constructor(e = !1) {
    super(!0, e);
  }
  set(e, n) {
    return !0;
  }
  deleteProperty(e, n) {
    return !0;
  }
}
const de = /* @__PURE__ */ new fe(), he = /* @__PURE__ */ new pe(), tt = (t) => t, H = (t) => Reflect.getPrototypeOf(t);
function _e(t, e, n) {
  return function(...r) {
    const s = this.__v_raw, i = /* @__PURE__ */ u(s), o = z(i), c = t === "entries" || t === Symbol.iterator && o, l = t === "keys" && o, a = s[t](...r), f = n ? tt : e ? K : w;
    return !e && _(
      i,
      "iterate",
      l ? k : E
    ), $(
      // inheriting all iterator properties
      Object.create(a),
      {
        // iterator protocol
        next() {
          const { value: p, done: b } = a.next();
          return b ? { value: p, done: b } : {
            value: c ? [f(p[0]), f(p[1])] : f(p),
            done: b
          };
        }
      }
    );
  };
}
function W(t) {
  return function(...e) {
    return t === "delete" ? !1 : t === "clear" ? void 0 : this;
  };
}
function ge(t, e) {
  const n = {
    get(s) {
      const i = this.__v_raw, o = /* @__PURE__ */ u(i), c = /* @__PURE__ */ u(s);
      t || (y(s, c) && _(o, "get", s), _(o, "get", c));
      const { has: l } = H(o), a = e ? tt : t ? K : w;
      if (l.call(o, s))
        return a(i.get(s));
      if (l.call(o, c))
        return a(i.get(c));
      i !== o && i.get(s);
    },
    get size() {
      const s = this.__v_raw;
      return !t && _(/* @__PURE__ */ u(s), "iterate", E), s.size;
    },
    has(s) {
      const i = this.__v_raw, o = /* @__PURE__ */ u(i), c = /* @__PURE__ */ u(s);
      return t || (y(s, c) && _(o, "has", s), _(o, "has", c)), s === c ? i.has(s) : i.has(s) || i.has(c);
    },
    forEach(s, i) {
      const o = this, c = o.__v_raw, l = /* @__PURE__ */ u(c), a = e ? tt : t ? K : w;
      return !t && _(l, "iterate", E), c.forEach((f, p) => s.call(i, a(f), a(p), o));
    }
  };
  return $(
    n,
    t ? {
      add: W("add"),
      set: W("set"),
      delete: W("delete"),
      clear: W("clear")
    } : {
      add(s) {
        const i = /* @__PURE__ */ u(this), o = H(i), c = /* @__PURE__ */ u(s), l = !e && !/* @__PURE__ */ A(s) && !/* @__PURE__ */ C(s) ? c : s;
        return o.has.call(i, l) || y(s, l) && o.has.call(i, s) || y(c, l) && o.has.call(i, c) || (i.add(l), R(i, "add", l, l)), this;
      },
      set(s, i) {
        !e && !/* @__PURE__ */ A(i) && !/* @__PURE__ */ C(i) && (i = /* @__PURE__ */ u(i));
        const o = /* @__PURE__ */ u(this), { has: c, get: l } = H(o);
        let a = c.call(o, s);
        a || (s = /* @__PURE__ */ u(s), a = c.call(o, s));
        const f = l.call(o, s);
        return o.set(s, i), a ? y(i, f) && R(o, "set", s, i) : R(o, "add", s, i), this;
      },
      delete(s) {
        const i = /* @__PURE__ */ u(this), { has: o, get: c } = H(i);
        let l = o.call(i, s);
        l || (s = /* @__PURE__ */ u(s), l = o.call(i, s)), c && c.call(i, s);
        const a = i.delete(s);
        return l && R(i, "delete", s, void 0), a;
      },
      clear() {
        const s = /* @__PURE__ */ u(this), i = s.size !== 0, o = s.clear();
        return i && R(
          s,
          "clear",
          void 0,
          void 0
        ), o;
      }
    }
  ), [
    "keys",
    "values",
    "entries",
    Symbol.iterator
  ].forEach((s) => {
    n[s] = _e(s, t, e);
  }), n;
}
function At(t, e) {
  const n = ge(t, e);
  return (r, s, i) => s === "__v_isReactive" ? !t : s === "__v_isReadonly" ? t : s === "__v_raw" ? r : Reflect.get(
    X(n, s) && s in r ? n : r,
    s,
    i
  );
}
const be = {
  get: /* @__PURE__ */ At(!1, !1)
}, me = {
  get: /* @__PURE__ */ At(!0, !1)
}, Et = /* @__PURE__ */ new WeakMap(), ve = /* @__PURE__ */ new WeakMap(), Dt = /* @__PURE__ */ new WeakMap(), Se = /* @__PURE__ */ new WeakMap();
function xe(t) {
  switch (t) {
    case "Object":
    case "Array":
      return 1;
    case "Map":
    case "Set":
    case "WeakMap":
    case "WeakSet":
      return 2;
    default:
      return 0;
  }
}
// @__NO_SIDE_EFFECTS__
function Ot(t) {
  return /* @__PURE__ */ C(t) ? t : Mt(
    t,
    !1,
    de,
    be,
    Et
  );
}
// @__NO_SIDE_EFFECTS__
function et(t) {
  return Mt(
    t,
    !0,
    he,
    me,
    Dt
  );
}
function Mt(t, e, n, r, s) {
  if (!m(t) || t.__v_raw && !(e && t.__v_isReactive) || t.__v_skip || !Object.isExtensible(t))
    return t;
  const i = s.get(t);
  if (i)
    return i;
  const o = xe(Qt(t));
  if (o === 0)
    return t;
  const c = new Proxy(
    t,
    o === 2 ? r : n
  );
  return s.set(t, c), c;
}
// @__NO_SIDE_EFFECTS__
function jt(t) {
  return /* @__PURE__ */ C(t) ? /* @__PURE__ */ jt(t.__v_raw) : !!(t && t.__v_isReactive);
}
// @__NO_SIDE_EFFECTS__
function C(t) {
  return !!(t && t.__v_isReadonly);
}
// @__NO_SIDE_EFFECTS__
function A(t) {
  return !!(t && t.__v_isShallow);
}
// @__NO_SIDE_EFFECTS__
function _t(t) {
  return t ? !!t.__v_raw : !1;
}
// @__NO_SIDE_EFFECTS__
function u(t) {
  const e = t && t.__v_raw;
  return e ? /* @__PURE__ */ u(e) : t;
}
const w = (t) => m(t) ? /* @__PURE__ */ Ot(t) : t, K = (t) => m(t) ? /* @__PURE__ */ et(t) : t;
// @__NO_SIDE_EFFECTS__
function D(t) {
  return t ? t.__v_isRef === !0 : !1;
}
// @__NO_SIDE_EFFECTS__
function Q(t) {
  return we(t, !1);
}
function we(t, e) {
  return /* @__PURE__ */ D(t) ? t : new ye(t, e);
}
class ye {
  constructor(e, n) {
    this.dep = new dt(), this.__v_isRef = !0, this.__v_isShallow = !1, this._rawValue = n ? e : /* @__PURE__ */ u(e), this._value = n ? e : w(e), this.__v_isShallow = n;
  }
  get value() {
    return this.dep.track(), this._value;
  }
  set value(e) {
    const n = this._rawValue, r = this.__v_isShallow || /* @__PURE__ */ A(e) || /* @__PURE__ */ C(e);
    e = r ? e : /* @__PURE__ */ u(e), y(e, n) && (this._rawValue = e, this._value = r ? e : w(e), this.dep.trigger());
  }
}
class Re {
  constructor(e, n, r) {
    this.fn = e, this.setter = n, this._value = void 0, this.dep = new dt(this), this.__v_isRef = !0, this.deps = void 0, this.depsTail = void 0, this.flags = 16, this.globalVersion = N - 1, this.next = void 0, this.effect = this, this.__v_isReadonly = !n, this.isSSR = r;
  }
  /**
   * @internal
   */
  notify() {
    if (this.flags |= 16, !(this.flags & 8) && // avoid infinite self recursion
    d !== this)
      return ee(this, !0), !0;
  }
  get value() {
    const e = this.dep.track();
    return wt(this), e && (e.version = this.dep.version), this._value;
  }
  set value(e) {
    this.setter && this.setter(e);
  }
}
// @__NO_SIDE_EFFECTS__
function Te(t, e, n = !1) {
  let r, s;
  return S(t) ? r = t : (r = t.get, s = t.set), new Re(r, s, n);
}
/**
* @vue/runtime-core v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
function Pt(t, e, n, r) {
  try {
    return r ? t(...r) : t();
  } catch (s) {
    Nt(s, e, n);
  }
}
function Ft(t, e, n, r) {
  if (S(t)) {
    const s = Pt(t, e, n, r);
    return s && qt(s) && s.catch((i) => {
      Nt(i, e, n);
    }), s;
  }
  if (g(t)) {
    const s = [];
    for (let i = 0; i < t.length; i++)
      s.push(Ft(t[i], e, n, r));
    return s;
  }
}
function Nt(t, e, n, r = !0) {
  const s = e ? e.vnode : null, { errorHandler: i, throwUnhandledErrorInProduction: o } = e && e.appContext.config || Ut;
  if (e) {
    let c = e.parent;
    const l = e.proxy, a = `https://vuejs.org/error-reference/#runtime-${n}`;
    for (; c; ) {
      const f = c.ec;
      if (f) {
        for (let p = 0; p < f.length; p++)
          if (f[p](t, l, a) === !1)
            return;
      }
      c = c.parent;
    }
    if (i) {
      ft(), Pt(i, null, 10, [
        t,
        l,
        a
      ]), pt();
      return;
    }
  }
  Ie(t, n, s, r, o);
}
function Ie(t, e, n, r = !0, s = !1) {
  if (s)
    throw t;
  console.error(t);
}
let U = null, Ce = null;
const gt = (t) => t.__isTeleport;
function Ae(t) {
  let e = t[0];
  if (t.length > 1) {
    for (const n of t)
      if (n.type !== Wt) {
        e = n;
        break;
      }
  }
  return e;
}
function Ee(t) {
  if (!De(t))
    return gt(t.type) && t.children ? Ae(t.children) : t;
  if (t.component)
    return t.component.subTree;
  const { shapeFlag: e, children: n } = t;
  if (n) {
    if (e & 16)
      return n[0];
    if (e & 32 && S(n.default))
      return n.default();
  }
}
function Lt(t, e) {
  if (t.shapeFlag & 6 && t.component) {
    t.transition = e;
    const n = t.component.subTree;
    Lt(
      gt(n.type) && Ee(n) || n,
      e
    );
  } else t.shapeFlag & 128 ? (t.ssContent.transition = e.clone(t.ssContent), t.ssFallback.transition = e.clone(t.ssFallback)) : t.transition = e;
}
ot().requestIdleCallback;
ot().cancelIdleCallback;
const De = (t) => t.type.__isKeepAlive;
function Oe(t, e, n = G, r = !1) {
  if (n) {
    const s = n[t] || (n[t] = []), i = e.__weh || (e.__weh = (...o) => {
      ft();
      const c = Be(n), l = Ft(e, n, t, o);
      return c(), pt(), l;
    });
    return r ? s.unshift(i) : s.push(i), i;
  }
}
const Kt = (t) => (e, n = G) => {
  (!bt || t === "sp") && Oe(t, (...r) => e(...r), n);
}, Me = Kt("m"), je = Kt("um"), Pe = /* @__PURE__ */ Symbol.for("v-ndc"), Fe = {}, Vt = (t) => Object.getPrototypeOf(t) === Fe, Ne = (t) => t.__isSuspense, Ht = /* @__PURE__ */ Symbol.for("v-fgt"), Le = /* @__PURE__ */ Symbol.for("v-txt"), Wt = /* @__PURE__ */ Symbol.for("v-cmt");
function nt(t) {
  return t ? t.__v_isVNode === !0 : !1;
}
const zt = ({ key: t }) => t ?? null, B = ({
  ref: t,
  ref_key: e,
  ref_for: n
}) => (typeof t == "number" && (t = "" + t), t != null ? I(t) || /* @__PURE__ */ D(t) || S(t) ? { i: U, r: t, k: e, f: !!n } : t : null);
function Ke(t, e = null, n = null, r = 0, s = null, i = t === Ht ? 0 : 1, o = !1, c = !1) {
  const l = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: t,
    props: e,
    key: e && zt(e),
    ref: e && B(e),
    scopeId: Ce,
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
    patchFlag: r,
    dynamicProps: s,
    dynamicChildren: null,
    appContext: null,
    ctx: U
  };
  return c ? (Y(l, n), i & 128 && t.normalize(l)) : n && (l.shapeFlag |= I(n) ? 8 : 16), l;
}
const j = Ve;
function Ve(t, e = null, n = null, r = 0, s = null, i = !1) {
  if ((!t || t === Pe) && (t = Wt), nt(t)) {
    const c = st(
      t,
      e,
      !0
      /* mergeRef: true */
    );
    return n && Y(c, n), c.patchFlag = -2, c;
  }
  if (Ue(t) && (t = t.__vccOpts), e) {
    e = He(e);
    let { class: c, style: l } = e;
    c && !I(c) && (e.class = lt(c)), m(l) && (/* @__PURE__ */ _t(l) && !g(l) && (l = $({}, l)), e.style = ct(l));
  }
  const o = I(t) ? 1 : Ne(t) ? 128 : gt(t) ? 64 : m(t) ? 4 : S(t) ? 2 : 0;
  return Ke(
    t,
    e,
    n,
    r,
    s,
    o,
    i,
    !0
  );
}
function He(t) {
  return t ? /* @__PURE__ */ _t(t) || Vt(t) ? $({}, t) : t : null;
}
function st(t, e, n = !1, r = !1) {
  const { props: s, ref: i, patchFlag: o, children: c, transition: l } = t, a = e ? ze(s || {}, e) : s, f = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: t.type,
    props: a,
    key: a && zt(a),
    ref: e && e.ref ? (
      // #2078 in the case of <component :is="vnode" ref="extra"/>
      // if the vnode itself already has a ref, cloneVNode will need to merge
      // the refs so the single vnode can be set on multiple refs
      n && i ? g(i) ? i.concat(B(e)) : [i, B(e)] : B(e)
    ) : i,
    scopeId: t.scopeId,
    slotScopeIds: t.slotScopeIds,
    children: c,
    target: t.target,
    targetStart: t.targetStart,
    targetAnchor: t.targetAnchor,
    staticCount: t.staticCount,
    shapeFlag: t.shapeFlag,
    // if the vnode is cloned with extra props, we can no longer assume its
    // existing patch flag to be reliable and need to add the FULL_PROPS flag.
    // note: preserve flag for fragments since they use the flag for children
    // fast paths only.
    patchFlag: e && t.type !== Ht ? o === -1 ? 16 : o | 16 : o,
    dynamicProps: t.dynamicProps,
    dynamicChildren: t.dynamicChildren,
    appContext: t.appContext,
    dirs: t.dirs,
    transition: l,
    // These should technically only be non-null on mounted VNodes. However,
    // they *should* be copied for kept-alive vnodes. So we just always copy
    // them since them being non-null during a mount doesn't affect the logic as
    // they will simply be overwritten.
    component: t.component,
    suspense: t.suspense,
    ssContent: t.ssContent && st(t.ssContent),
    ssFallback: t.ssFallback && st(t.ssFallback),
    placeholder: t.placeholder,
    el: t.el,
    anchor: t.anchor,
    ctx: t.ctx,
    ce: t.ce
  };
  return l && r && Lt(
    f,
    l.clone(f)
  ), f;
}
function We(t = " ", e = 0) {
  return j(Le, null, t, e);
}
function Y(t, e) {
  let n = 0;
  const { shapeFlag: r } = t;
  if (e == null)
    e = null;
  else if (g(e))
    n = 16;
  else if (typeof e == "object")
    if (r & 65) {
      const s = e.default;
      s && (s._c && (s._d = !1), Y(t, s()), s._c && (s._d = !0));
      return;
    } else
      n = 32, !e._ && !Vt(e) && (e._ctx = U);
  else if (S(e)) {
    if (r & 65) {
      Y(t, { default: e });
      return;
    }
    e = { default: e, _ctx: U }, n = 32;
  } else
    e = String(e), r & 64 ? (n = 16, e = [We(e)]) : n = 8;
  t.children = e, t.shapeFlag |= n;
}
function ze(...t) {
  const e = {};
  for (let n = 0; n < t.length; n++) {
    const r = t[n];
    for (const s in r)
      if (s === "class")
        e.class !== r.class && (e.class = lt([e.class, r.class]));
      else if (s === "style")
        e.style = ct([e.style, r.style]);
      else if (Yt(s)) {
        const i = e[s], o = r[s];
        o && i !== o && !(g(i) && i.includes(o)) ? e[s] = i ? [].concat(i, o) : o : o == null && i == null && // mergeProps({ 'onUpdate:modelValue': undefined }) should not retain
        // the model listener.
        !$t(s) && (e[s] = o);
      } else s !== "" && (e[s] = r[s]);
  }
  return e;
}
let G = null, rt;
{
  const t = ot(), e = (n, r) => {
    let s;
    return (s = t[n]) || (s = t[n] = []), s.push(r), (i) => {
      s.length > 1 ? s.forEach((o) => o(i)) : s[0](i);
    };
  };
  rt = e(
    "__VUE_INSTANCE_SETTERS__",
    (n) => G = n
  ), e(
    "__VUE_SSR_SETTERS__",
    (n) => bt = n
  );
}
const Be = (t) => {
  const e = G;
  return rt(t), t.scope.on(), () => {
    t.scope.off(), rt(e);
  };
};
let bt = !1;
function Ue(t) {
  return S(t) && "__vccOpts" in t;
}
const Ye = (t, e) => /* @__PURE__ */ Te(t, e, bt);
function h(t, e, n) {
  try {
    const r = arguments.length;
    return r === 2 ? m(e) && !g(e) ? nt(e) ? j(t, null, [e]) : j(t, e) : j(t, null, e) : (r > 3 ? n = Array.prototype.slice.call(arguments, 2) : r === 3 && nt(n) && (n = [n]), j(t, e, n));
  } finally {
  }
}
const $e = {
  pending: "待办",
  in_progress: "进行中",
  completed: "已完成",
  deleted: "已删"
}, Ge = {
  pending: "#7d838f",
  in_progress: "#d4a25c",
  completed: "#7dd87d",
  deleted: "#ff7b72"
}, Je = {
  name: "TodoPanel",
  props: {
    state: { type: Object, default: () => ({}) }
  },
  setup() {
    const t = /* @__PURE__ */ Q([]), e = /* @__PURE__ */ Q(""), n = /* @__PURE__ */ Q(!0);
    let r = null;
    async function s() {
      try {
        const o = await fetch("/api/todo");
        if (!o.ok) {
          e.value = `HTTP ${o.status}`;
          return;
        }
        const c = await o.json();
        Array.isArray(c) ? (t.value = c, e.value = "") : e.value = c.error ?? "未知错误";
      } catch (o) {
        e.value = String(o);
      }
    }
    const i = Ye(() => {
      const o = { pending: 0, in_progress: 0, completed: 0 };
      for (const c of t.value)
        c.status in o && o[c.status]++;
      return o;
    });
    return Me(() => {
      s(), r = setInterval(() => void s(), 5e3);
    }), je(() => {
      r && clearInterval(r);
    }), { todos: t, err: e, open: n, refresh: s, counts: i };
  },
  render() {
    const t = this, e = t.state ?? {}, n = h(
      "div",
      { style: { display: "flex", alignItems: "center", gap: "10px", fontSize: "12px", color: "#7d838f" } },
      [
        h("span", { style: { color: "#6eb3ff", fontWeight: 600 } }, "gah"),
        h("span", { style: { color: e.running ? "#d4a25c" : "#7d838f" } }, e.running ? "思考/执行" : "待输入"),
        h("span", {}, "模型 " + (e.model || "未设置")),
        h("span", {}, "[" + e.thinking + "]"),
        h("span", { style: { color: "#7dd87d" } }, `✓${t.counts.completed}`),
        h("span", { style: { color: "#d4a25c" } }, `▶${t.counts.in_progress}`),
        h("span", { onClick: () => {
          t.open = !t.open;
        } }, t.open ? "▾ 收起" : "▸ 展开")
      ]
    ), r = t.open ? h(
      "aside",
      {
        style: {
          position: "fixed",
          right: "14px",
          bottom: "44px",
          width: "300px",
          maxHeight: "320px",
          overflowY: "auto",
          background: "#1b1e24",
          border: "1px solid #2a2e36",
          borderRadius: "8px",
          padding: "10px",
          zIndex: 15,
          fontSize: "12px",
          boxShadow: "0 8px 24px rgba(0,0,0,.45)"
        }
      },
      [
        h("div", { style: { display: "flex", justifyContent: "space-between", marginBottom: "6px" } }, [
          h("b", { style: { color: "#c9cdd6" } }, "任务面板(todo)"),
          h("button", { onClick: () => void t.refresh(), style: qe }, "↻ 刷新")
        ]),
        t.err ? h("div", { style: { color: "#ff7b72" } }, t.err) : t.todos.length === 0 ? h("div", { style: { color: "#7d838f" } }, "暂无任务(模型可经 todo 工具建单)") : t.todos.map(
          (s) => h(
            "div",
            {
              key: s.id,
              style: {
                display: "flex",
                gap: "8px",
                padding: "4px 0",
                borderBottom: "1px solid #2a2e36",
                color: "#c9cdd6"
              }
            },
            [
              h("span", { style: { color: Ge[s.status] ?? "#7d838f", fontWeight: 600 } }, $e[s.status] ?? s.status),
              h("span", { style: { flex: 1 } }, s.subject),
              s.status === "in_progress" && s.activeForm ? h("span", { style: { color: "#6eb3ff" } }, s.activeForm) : null
            ]
          )
        )
      ]
    ) : null;
    return h("div", null, [n, r]);
  }
}, qe = {
  background: "none",
  border: "1px solid #2a2e36",
  borderRadius: "4px",
  color: "#6eb3ff",
  cursor: "pointer",
  fontSize: "11px",
  padding: "1px 8px"
};
export {
  Je as default
};
