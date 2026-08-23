export type AccountFilters={query:string;status:''|'active'|'disabled';attention:''|'missing_room'|'missing_obs'};
export const emptyAccountFilters=():AccountFilters=>({query:'',status:'',attention:''});
