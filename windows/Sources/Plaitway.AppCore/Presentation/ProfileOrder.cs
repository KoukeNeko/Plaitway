namespace Plaitway.AppCore.Presentation;

/// <summary>The priority order of the profiles after a move; the daemon is sent the whole new order.</summary>
public static class ProfileOrder
{
    /// <summary>
    /// The ids after the items at <paramref name="fromOffsets"/> were moved in front of the item that was at
    /// <paramref name="toOffset"/>, as a list drag does it; <paramref name="toOffset"/> equal to the length moves them to the end.
    /// </summary>
    public static IReadOnlyList<string> Moving(IReadOnlyList<string> ids, IReadOnlyCollection<int> fromOffsets, int toOffset)
    {
        var moved = fromOffsets.Order().Select(offset => ids[offset]).ToList();
        var staying = ids.Where((_, offset) => !fromOffsets.Contains(offset)).ToList();
        var movedBeforeTarget = fromOffsets.Count(offset => offset < toOffset);
        staying.InsertRange(toOffset - movedBeforeTarget, moved);
        return staying;
    }

    /// <summary>
    /// The ids after <paramref name="id"/> moved by <paramref name="delta"/> places (-1 up, +1 down); null at either end
    /// or for an id that is not in the list.
    /// </summary>
    public static IReadOnlyList<string>? Moving(IReadOnlyList<string> ids, string id, int delta)
    {
        var index = ids.ToList().IndexOf(id);
        var target = index + delta;
        if (index < 0 || target < 0 || target >= ids.Count)
        {
            return null;
        }

        var moved = ids.ToList();
        (moved[index], moved[target]) = (moved[target], moved[index]);
        return moved;
    }
}
