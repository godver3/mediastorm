export interface SeasonalListConfig {
  id: string;
  name: string;
  mdblistUrl: string;
  startMonth: number;
  startDay: number;
  endMonth: number;
  endDay: number;
}

export const SEASONAL_LISTS: SeasonalListConfig[] = [
  {
    id: 'valentines',
    name: "Valentine's Day Movies",
    mdblistUrl: 'https://mdblist.com/lists/linvo/valentines-day-popular-movies',
    startMonth: 2,
    startDay: 1,
    endMonth: 2,
    endDay: 28,
  },
  {
    id: 'oscars',
    name: 'Academy Award Winners',
    mdblistUrl: 'https://mdblist.com/lists/atb001/academy-award-best-actor-actress-director-picture',
    startMonth: 1,
    startDay: 15,
    endMonth: 3,
    endDay: 31,
  },
  {
    id: 'summer-blockbusters',
    name: 'Summer Blockbusters',
    mdblistUrl: 'https://mdblist.com/lists/mdiezka/the-65-best-summer-movies-of-all-time',
    startMonth: 6,
    startDay: 1,
    endMonth: 8,
    endDay: 31,
  },
  {
    id: 'coming-of-age',
    name: 'Coming of Age',
    mdblistUrl: 'https://mdblist.com/lists/galacticboy/coming-of-age',
    startMonth: 8,
    startDay: 15,
    endMonth: 9,
    endDay: 30,
  },
  {
    id: 'halloween',
    name: 'Halloween Horror',
    mdblistUrl: 'https://mdblist.com/lists/linaspuransen/top-100-horror-movies',
    startMonth: 10,
    startDay: 1,
    endMonth: 11,
    endDay: 1,
  },
  {
    id: 'thanksgiving',
    name: 'Thanksgiving Movies',
    mdblistUrl: 'https://mdblist.com/lists/hdlists/thanksgiving-movies',
    startMonth: 11,
    startDay: 1,
    endMonth: 11,
    endDay: 28,
  },
  {
    id: 'christmas',
    name: 'Christmas Movies',
    mdblistUrl: 'https://mdblist.com/lists/linaspuransen/top-100-christmas-movies',
    startMonth: 11,
    startDay: 25,
    endMonth: 1,
    endDay: 7,
  },
];

/** Returns seasonal lists active for the current date */
export function getActiveSeasonalLists(): SeasonalListConfig[] {
  const now = new Date();
  const month = now.getMonth() + 1; // 1-indexed
  const day = now.getDate();

  return SEASONAL_LISTS.filter((list) => {
    if (list.startMonth <= list.endMonth) {
      // Same-year range (e.g., Oct 1 - Nov 1)
      if (month > list.startMonth || (month === list.startMonth && day >= list.startDay)) {
        if (month < list.endMonth || (month === list.endMonth && day <= list.endDay)) {
          return true;
        }
      }
      return false;
    }
    // Cross-year range (e.g., Nov 25 - Jan 7)
    if (month > list.startMonth || (month === list.startMonth && day >= list.startDay)) {
      return true;
    }
    if (month < list.endMonth || (month === list.endMonth && day <= list.endDay)) {
      return true;
    }
    return false;
  });
}
