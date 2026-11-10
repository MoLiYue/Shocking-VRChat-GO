import{c as d,d as r,r as i,a as l,b as o,k as v,u as n,n as p,m as u,t as m,B as h,Q as _,R as f,e as b,y as g,_ as S}from"./index-C93lZyxG.js";/**
 * @license lucide-vue-next v1.0.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const k=d("chevron-right",[["path",{d:"m9 18 6-6-6-6",key:"mthhwq"}]]),y={class:"advanced"},x=["aria-expanded"],B=r({__name:"AdvancedSection",props:{title:{}},setup(C){const{t:c}=g(),e=i(!1),a=`adv-${Math.random().toString(36).slice(2,8)}`;return(t,s)=>(b(),l("div",y,[o("button",{type:"button",class:"btn btn-ghost btn-sm toggle","aria-expanded":e.value,"aria-controls":a,onClick:s[0]||(s[0]=w=>e.value=!e.value)},[v(n(k),{size:14,class:p(["chev",{open:e.value}])},null,8,["class"]),u(" "+m(t.title||n(c)("common.advanced")),1)],8,x),h(o("div",{id:a,class:"body"},[f(t.$slots,"default",{},void 0)],512),[[_,e.value]])]))}}),I=S(B,[["__scopeId","data-v-fa93ad64"]]);export{I as A};
